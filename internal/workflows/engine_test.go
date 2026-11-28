package workflows_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/workflows"
)

type fakeAgentRunner struct {
	mu        sync.Mutex
	calls     []workflows.AgentStepRequest
	cancelled []string
	fn        func(ctx context.Context, req workflows.AgentStepRequest) (string, string, error)
}

func (f *fakeAgentRunner) RunAgentStep(ctx context.Context, req workflows.AgentStepRequest) (string, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	sid := req.SessionID
	if sid == "" {
		sid = "ses_" + req.StepName
	}
	if req.OnSession != nil {
		req.OnSession(sid)
	}
	return sid, "agent-out:" + req.Prompt, nil
}

func (f *fakeAgentRunner) CancelSession(_ context.Context, sessionID string) error {
	f.mu.Lock()
	f.cancelled = append(f.cancelled, sessionID)
	f.mu.Unlock()
	return nil
}

type fakeShellRunner struct {
	mu    sync.Mutex
	calls []workflows.ShellStepRequest
	fn    func(ctx context.Context, req workflows.ShellStepRequest) (string, int, error)
}

func (f *fakeShellRunner) RunShellStep(ctx context.Context, req workflows.ShellStepRequest) (string, int, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req)
	fn := f.fn
	f.mu.Unlock()
	if fn != nil {
		return fn(ctx, req)
	}
	return "shell-out:" + req.Command, 0, nil
}

func setupWorkflowStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.CreateUser(t.Context(), db.CreateUserParams{
		ID: "u1", Name: "Alice", Email: "alice@example.com", CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
	return st
}

func createWorkflow(t *testing.T, st *store.Store, id, name string, def workflows.Definition) {
	t.Helper()
	if err := def.Validate(time.Now()); err != nil {
		t.Fatalf("validate def: %v", err)
	}
	b, err := json.Marshal(def)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CreateWorkflow(t.Context(), db.CreateWorkflowParams{
		ID: id, OwnerID: "u1", Name: name, DefinitionJson: string(b), Enabled: 1, CreatedAt: 100, UpdatedAt: 100,
	}); err != nil {
		t.Fatal(err)
	}
}

func waitForRunStatus(t *testing.T, eng *workflows.Engine, runID, want string) workflows.RunDetail {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		d, err := eng.GetRun(t.Context(), runID, "u1")
		if err == nil && d.Run.Status == want {
			return d
		}
		time.Sleep(15 * time.Millisecond)
	}
	d, _ := eng.GetRun(t.Context(), runID, "u1")
	t.Fatalf("timed out waiting for run %s status=%s (got %s, err=%q)", runID, want, d.Run.Status, d.Run.Error)
	return d
}

func TestMultiStepAgentShellAskAndDurableRestart(t *testing.T) {
	st := setupWorkflowStore(t)
	agents := &fakeAgentRunner{}
	shell := &fakeShellRunner{}

	createWorkflow(t, st, "wfl_rel", "Release", workflows.Definition{
		Steps: []workflows.StepDef{
			{Name: "triage", Kind: workflows.StepKindAgent, AgentID: "ag1", Prompt: "Triage {{input.repo}}"},
			{Name: "verify", Kind: workflows.StepKindShell, AgentID: "ag1", Command: "check {{steps.triage.output}}"},
			{Name: "approve", Kind: workflows.StepKindAsk, AskTitle: "Ship {{input.repo}}? {{steps.verify.output}}", AskChoices: []string{"Ship", "Hold"}, ContinueOnChoices: []string{"Ship"}},
			{Name: "publish", Kind: workflows.StepKindAgent, AgentID: "ag1", ReuseSession: true, Prompt: "Publish after {{steps.approve.output}}"},
		},
	})

	eng1 := workflows.NewEngine(workflows.Config{Store: st, Agents: agents, Shell: shell})
	run, err := eng1.StartRun(t.Context(), "wfl_rel", "u1", workflows.TriggerManual, map[string]any{"repo": "agenthold"})
	if err != nil {
		t.Fatal(err)
	}

	paused := waitForRunStatus(t, eng1, run.Run.ID, workflows.RunNeedsInput)
	if paused.Steps[0].Status != workflows.StepCompleted || paused.Steps[1].Status != workflows.StepCompleted || paused.Steps[2].Status != workflows.StepWaiting {
		t.Fatalf("unexpected step statuses before restart: %+v", paused.Steps)
	}
	if !strings.Contains(paused.Steps[2].AskTitle, "Ship agenthold? shell-out:check agent-out:Triage agenthold") {
		t.Fatalf("template substitution in ask title failed: %q", paused.Steps[2].AskTitle)
	}

	// Simulate server restart: close eng1, create brand-new eng2 on same store.
	eng1.Close()
	eng2 := workflows.NewEngine(workflows.Config{Store: st, Agents: agents, Shell: shell})
	defer eng2.Close()
	if err := eng2.Start(t.Context()); err != nil {
		t.Fatal(err)
	}

	// Answer the ask step on eng2; step 4 should reuse session from step 1 and complete.
	if _, err := eng2.AnswerStep(t.Context(), run.Run.ID, paused.Steps[2].ID, "u1", "Ship", "LGTM"); err != nil {
		t.Fatal(err)
	}
	done := waitForRunStatus(t, eng2, run.Run.ID, workflows.RunCompleted)
	if done.Steps[3].Status != workflows.StepCompleted {
		t.Fatalf("step 4 status = %s", done.Steps[3].Status)
	}
	if done.Steps[3].SessionID != done.Steps[0].SessionID {
		t.Fatalf("expected reused session %q, got %q", done.Steps[0].SessionID, done.Steps[3].SessionID)
	}
	if !strings.Contains(done.Steps[3].Output, "Publish after Ship: LGTM") {
		t.Fatalf("unexpected step 4 output: %q", done.Steps[3].Output)
	}

	// Rejecting on ContinueOnChoices cancels cleanly and skips remaining steps.
	run2, err := eng2.StartRun(t.Context(), "wfl_rel", "u1", workflows.TriggerManual, map[string]any{"repo": "demo"})
	if err != nil {
		t.Fatal(err)
	}
	paused2 := waitForRunStatus(t, eng2, run2.Run.ID, workflows.RunNeedsInput)
	cancelled, err := eng2.AnswerStep(t.Context(), run2.Run.ID, paused2.Steps[2].ID, "u1", "Hold", "not ready")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.Run.Status != workflows.RunCancelled || cancelled.Steps[3].Status != workflows.StepSkipped {
		t.Fatalf("expected cancelled + skipped, got run=%s step4=%s", cancelled.Run.Status, cancelled.Steps[3].Status)
	}
}

func TestFailureResumeAndCancel(t *testing.T) {
	st := setupWorkflowStore(t)
	var step1Count, step2Count atomic.Int32
	agents := &fakeAgentRunner{
		fn: func(_ context.Context, req workflows.AgentStepRequest) (string, string, error) {
			if req.StepName == "first" {
				step1Count.Add(1)
				return "ses_1", "ok-first", nil
			}
			n := step2Count.Add(1)
			if n == 1 {
				return "ses_2", "", errors.New("transient boom")
			}
			return "ses_2", "ok-second:" + req.Prompt, nil
		},
	}

	createWorkflow(t, st, "wfl_res", "Resumable", workflows.Definition{
		Steps: []workflows.StepDef{
			{Name: "first", Kind: workflows.StepKindAgent, AgentID: "ag1", Prompt: "go"},
			{Name: "second", Kind: workflows.StepKindAgent, AgentID: "ag1", Prompt: "after {{steps.first.output}}"},
		},
	})

	eng := workflows.NewEngine(workflows.Config{Store: st, Agents: agents})
	defer eng.Close()

	run, err := eng.StartRun(t.Context(), "wfl_res", "u1", workflows.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := waitForRunStatus(t, eng, run.Run.ID, workflows.RunFailed)
	if failed.Steps[0].Status != workflows.StepCompleted || failed.Steps[1].Status != workflows.StepFailed {
		t.Fatalf("unexpected failed state: %+v", failed.Steps)
	}

	// Resume should NOT re-run step 1; it retries only step 2 and succeeds.
	if _, err := eng.ResumeRun(t.Context(), run.Run.ID, "u1"); err != nil {
		t.Fatal(err)
	}
	done := waitForRunStatus(t, eng, run.Run.ID, workflows.RunCompleted)
	if step1Count.Load() != 1 || step2Count.Load() != 2 {
		t.Fatalf("expected step1=1 step2=2, got %d and %d", step1Count.Load(), step2Count.Load())
	}
	if !strings.Contains(done.Steps[1].Output, "ok-second:after ok-first") {
		t.Fatalf("unexpected resumed output: %q", done.Steps[1].Output)
	}

	// Cancel a running step.
	started := make(chan struct{})
	blocking := &fakeAgentRunner{
		fn: func(ctx context.Context, req workflows.AgentStepRequest) (string, string, error) {
			if req.OnSession != nil {
				req.OnSession("ses_block")
			}
			close(started)
			<-ctx.Done()
			return "ses_block", "", ctx.Err()
		},
	}
	engCancel := workflows.NewEngine(workflows.Config{Store: st, Agents: blocking})
	defer engCancel.Close()
	createWorkflow(t, st, "wfl_can", "Cancellable", workflows.Definition{
		Steps: []workflows.StepDef{
			{Name: "slow", Kind: workflows.StepKindAgent, AgentID: "ag1", Prompt: "wait"},
		},
	})
	cRun, err := engCancel.StartRun(t.Context(), "wfl_can", "u1", workflows.TriggerManual, nil)
	if err != nil {
		t.Fatal(err)
	}
	<-started
	cRes, err := engCancel.CancelRun(t.Context(), cRun.Run.ID, "u1")
	if err != nil {
		t.Fatal(err)
	}
	if cRes.Run.Status != workflows.RunCancelled {
		t.Fatalf("expected cancelled, got %s", cRes.Run.Status)
	}
	engCancel.Wait()
}

func TestConcurrencyLimitQueueingAndPromotion(t *testing.T) {
	st := setupWorkflowStore(t)
	releaseFirst := make(chan struct{})
	firstEntered := make(chan struct{})
	var runningNow, maxConcurrent atomic.Int32

	agents := &fakeAgentRunner{
		fn: func(_ context.Context, req workflows.AgentStepRequest) (string, string, error) {
			cur := runningNow.Add(1)
			defer runningNow.Add(-1)
			for {
				prev := maxConcurrent.Load()
				if cur <= prev || maxConcurrent.CompareAndSwap(prev, cur) {
					break
				}
			}
			if strings.Contains(req.Prompt, "run-1") {
				close(firstEntered)
				<-releaseFirst
			}
			return "ses_" + req.RunID, "done:" + req.Prompt, nil
		},
	}

	createWorkflow(t, st, "wfl_conc", "Limited", workflows.Definition{
		ConcurrencyLimit: 1,
		Steps: []workflows.StepDef{
			{Name: "work", Kind: workflows.StepKindAgent, AgentID: "ag1", Prompt: "{{input.tag}}"},
		},
	})

	eng := workflows.NewEngine(workflows.Config{Store: st, Agents: agents})
	defer eng.Close()

	r1, err := eng.StartRun(t.Context(), "wfl_conc", "u1", workflows.TriggerManual, map[string]any{"tag": "run-1"})
	if err != nil {
		t.Fatal(err)
	}
	<-firstEntered

	r2, err := eng.StartRun(t.Context(), "wfl_conc", "u1", workflows.TriggerManual, map[string]any{"tag": "run-2"})
	if err != nil {
		t.Fatal(err)
	}
	if r2.Run.Status != workflows.RunQueued {
		t.Fatalf("expected run 2 to be queued while run 1 active, got %s", r2.Run.Status)
	}

	close(releaseFirst)
	waitForRunStatus(t, eng, r1.Run.ID, workflows.RunCompleted)
	waitForRunStatus(t, eng, r2.Run.ID, workflows.RunCompleted)

	if maxConcurrent.Load() > 1 {
		t.Fatalf("concurrency limit exceeded: max concurrent = %d", maxConcurrent.Load())
	}
}

func TestScheduleTickAndWebhookTrigger(t *testing.T) {
	st := setupWorkflowStore(t)
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC)
	token := "secret-workflow-hook-token"

	def := workflows.Definition{
		Schedule:         "*/5 * * * *",
		Timezone:         "UTC",
		WebhookEnabled:   true,
		WebhookTokenHash: auth.HashToken(token),
		Steps: []workflows.StepDef{
			{Name: "run", Kind: workflows.StepKindShell, AgentID: "ag1", Command: "echo {{input.event}}"},
		},
	}
	if err := def.Validate(now); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(def)
	if err := st.CreateWorkflow(t.Context(), db.CreateWorkflowParams{
		ID: "wfl_trig", OwnerID: "u1", Name: "Scheduled", DefinitionJson: string(b), Enabled: 1, CreatedAt: now.Unix(), UpdatedAt: now.Unix(),
	}); err != nil {
		t.Fatal(err)
	}

	shell := &fakeShellRunner{}
	eng := workflows.NewEngine(workflows.Config{
		Store: st,
		Shell: shell,
		Now:   func() time.Time { return now },
	})
	defer eng.Close()

	// Before next run (09:05): tick does nothing.
	eng.TickSchedules(t.Context())
	runs, _ := eng.ListRuns(t.Context(), "u1", "all", 50)
	if len(runs) != 0 {
		t.Fatalf("expected 0 runs before due time, got %d", len(runs))
	}

	// Advance past 09:05: tick fires scheduled run.
	now = now.Add(6 * time.Minute)
	eng.TickSchedules(t.Context())
	eng.Wait()
	runs, _ = eng.ListRuns(t.Context(), "u1", "all", 50)
	if len(runs) != 1 || runs[0].Run.TriggerKind != workflows.TriggerSchedule || runs[0].Run.Status != workflows.RunCompleted {
		t.Fatalf("expected 1 completed scheduled run, got %+v", runs)
	}

	// Webhook trigger with valid token fires webhook run; invalid token returns ErrNotFound.
	if _, err := eng.TriggerWebhook(t.Context(), auth.HashToken("wrong"), nil); !errors.Is(err, workflows.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for bad webhook token, got %v", err)
	}
	whRun, err := eng.TriggerWebhook(t.Context(), auth.HashToken(token), map[string]any{"event": "deploy"})
	if err != nil {
		t.Fatal(err)
	}
	done := waitForRunStatus(t, eng, whRun.Run.ID, workflows.RunCompleted)
	if done.Run.TriggerKind != workflows.TriggerWebhook || !strings.Contains(done.Steps[0].Output, fmt.Sprintf("echo %s", "deploy")) {
		t.Fatalf("unexpected webhook run result: %+v", done)
	}
}
