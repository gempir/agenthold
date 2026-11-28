package workflows

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/trigger"
)

// Config configures a workflow Engine.
type Config struct {
	Store  *store.Store
	Agents AgentRunner
	Shell  ShellRunner
	Now    func() time.Time
	Log    *slog.Logger
}

// Engine executes durable multi-stage workflows, manages concurrency limits,
// checkpoints step outputs across restarts, and ticks scheduled workflows.
type Engine struct {
	store  *store.Store
	agents AgentRunner
	shell  ShellRunner
	nowFn  func() time.Time
	lg     *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// concMu serializes concurrency slot checks and queued-run promotion.
	concMu sync.Mutex

	// mu guards in-memory execution handles only (never held across I/O).
	mu      sync.Mutex
	closing bool
	cancels map[string]context.CancelFunc
}

// NewEngine creates a new durable workflow engine.
func NewEngine(cfg Config) *Engine {
	lg := cfg.Log
	if lg == nil {
		lg = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Engine{
		store:   cfg.Store,
		agents:  cfg.Agents,
		shell:   cfg.Shell,
		nowFn:   cfg.Now,
		lg:      lg,
		ctx:     ctx,
		cancel:  cancel,
		cancels: map[string]context.CancelFunc{},
	}
}

func (e *Engine) now() time.Time {
	if e.nowFn != nil {
		return e.nowFn().UTC()
	}
	return time.Now().UTC()
}

// Start resumes any unfinished running/queued workflow runs persisted before a
// server restart. Runs paused in needs_input stay parked until answered.
func (e *Engine) Start(ctx context.Context) error {
	runs, err := e.store.ListActiveWorkflowRuns(ctx)
	if err != nil {
		return err
	}
	seenQueuedWf := map[string]bool{}
	for _, r := range runs {
		switch r.Status {
		case RunRunning:
			// Reset any in-flight step from the prior process back to pending so
			// it executes cleanly on resume while completed steps stay checkpointed.
			steps, err := e.store.ListWorkflowSteps(ctx, r.ID)
			if err != nil {
				e.lg.Warn("workflows: load steps on startup resume", "run", r.ID, "err", err)
				continue
			}
			for _, st := range steps {
				if st.Status == StepRunning {
					st.Status = StepPending
					_ = e.updateStep(ctx, st)
				}
			}
			e.launchRun(r.ID)
		case RunQueued:
			seenQueuedWf[r.WorkflowID] = true
		}
	}
	for wfID := range seenQueuedWf {
		e.promoteQueued(wfID)
	}
	return nil
}

// Close cancels active in-memory runs and waits for all background goroutines
// to exit cleanly.
func (e *Engine) Close() {
	e.mu.Lock()
	e.closing = true
	cancels := make([]context.CancelFunc, 0, len(e.cancels))
	for _, c := range e.cancels {
		cancels = append(cancels, c)
	}
	e.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	e.cancel()
	e.wg.Wait()
}

// Wait blocks until all currently active background run goroutines finish (tests).
func (e *Engine) Wait() { e.wg.Wait() }

// StepAnswer is stored in workflow_steps.answer_json when a human answers an ask.
type StepAnswer struct {
	Choice string `json:"choice"`
	Reason string `json:"reason,omitempty"`
}

// RunDetail bundles a workflow run, its workflow metadata, and ordered steps.
type RunDetail struct {
	Run          db.WorkflowRun
	WorkflowName string
	Definition   Definition
	Steps        []db.WorkflowStep
}

// StartRun creates a new durable run for workflowID and starts it immediately
// unless the workflow's concurrency_limit is currently full (in which case it
// stays queued in FIFO order).
func (e *Engine) StartRun(ctx context.Context, workflowID, ownerID, triggerKind string, input map[string]any) (RunDetail, error) {
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if ownerID != "" && wf.OwnerID != ownerID {
		return RunDetail{}, ErrNotFound
	}
	def, err := ParseDefinition(wf.DefinitionJson)
	if err != nil {
		return RunDetail{}, err
	}
	if len(def.Steps) == 0 {
		return RunDetail{}, fmt.Errorf("%w: workflow has no steps", ErrInvalidDefinition)
	}
	if triggerKind == "" {
		triggerKind = TriggerManual
	}
	if input == nil {
		input = map[string]any{}
	}
	inBytes, err := json.Marshal(input)
	if err != nil {
		return RunDetail{}, err
	}

	nowUnix := e.now().Unix()
	runID := id.New("wfr")

	e.concMu.Lock()
	startNow := true
	if def.ConcurrencyLimit > 0 {
		active, cerr := e.store.CountActiveRunsForWorkflow(ctx, wf.ID)
		if cerr != nil {
			e.concMu.Unlock()
			return RunDetail{}, cerr
		}
		if active >= int64(def.ConcurrencyLimit) {
			startNow = false
		}
	}

	status := RunQueued
	var startedAt sql.NullInt64
	if startNow {
		status = RunRunning
		startedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
	}

	runRow := db.WorkflowRun{
		ID:          runID,
		WorkflowID:  wf.ID,
		OwnerID:     wf.OwnerID,
		Status:      status,
		TriggerKind: triggerKind,
		InputJson:   string(inBytes),
		OutputJson:  "{}",
		Error:       "",
		CurrentStep: 0,
		StartedAt:   startedAt,
		CreatedAt:   nowUnix,
		UpdatedAt:   nowUnix,
	}
	if err := e.store.CreateWorkflowRun(ctx, db.CreateWorkflowRunParams(runRow)); err != nil {
		e.concMu.Unlock()
		return RunDetail{}, err
	}

	steps := make([]db.WorkflowStep, 0, len(def.Steps))
	for i, sd := range def.Steps {
		choicesBytes, _ := json.Marshal(sd.AskChoices)
		if len(sd.AskChoices) == 0 {
			choicesBytes = []byte("[]")
		}
		st := db.WorkflowStep{
			ID:             id.New("wfs"),
			RunID:          runID,
			StepIndex:      int64(i),
			Name:           sd.Name,
			Kind:           sd.Kind,
			Status:         StepPending,
			AskTitle:       sd.AskTitle,
			AskChoicesJson: string(choicesBytes),
		}
		if err := e.store.CreateWorkflowStep(ctx, db.CreateWorkflowStepParams(st)); err != nil {
			e.concMu.Unlock()
			return RunDetail{}, err
		}
		steps = append(steps, st)
	}
	e.concMu.Unlock()

	if startNow {
		e.launchRun(runID)
	}
	return RunDetail{
		Run:          runRow,
		WorkflowName: wf.Name,
		Definition:   def,
		Steps:        steps,
	}, nil
}

// GetRun returns one run and all its step checkpoints for ownerID.
func (e *Engine) GetRun(ctx context.Context, runID, ownerID string) (RunDetail, error) {
	r, err := e.store.GetWorkflowRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if ownerID != "" && r.OwnerID != ownerID {
		return RunDetail{}, ErrNotFound
	}
	return e.loadRunDetail(ctx, r)
}

// ListRuns lists workflow runs for ownerID filtered by status ("all", "needs_input", "running").
func (e *Engine) ListRuns(ctx context.Context, ownerID, statusFilter string, limit int64) ([]RunDetail, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	var rows []db.WorkflowRun
	var err error
	switch strings.TrimSpace(statusFilter) {
	case "", "all":
		rows, err = e.store.ListUserWorkflowRuns(ctx, db.ListUserWorkflowRunsParams{OwnerID: ownerID, Limit: limit})
	case RunNeedsInput:
		rows, err = e.store.ListUserWorkflowRunsByStatus(ctx, db.ListUserWorkflowRunsByStatusParams{OwnerID: ownerID, Status: RunNeedsInput, Limit: limit})
	case RunRunning:
		// Include both running and queued runs in the Running filter.
		all, aerr := e.store.ListUserWorkflowRuns(ctx, db.ListUserWorkflowRunsParams{OwnerID: ownerID, Limit: limit})
		err = aerr
		for _, r := range all {
			if r.Status == RunRunning || r.Status == RunQueued {
				rows = append(rows, r)
			}
		}
	default:
		rows, err = e.store.ListUserWorkflowRunsByStatus(ctx, db.ListUserWorkflowRunsByStatusParams{OwnerID: ownerID, Status: statusFilter, Limit: limit})
	}
	if err != nil {
		return nil, err
	}
	out := make([]RunDetail, 0, len(rows))
	for _, r := range rows {
		d, err := e.loadRunDetail(ctx, r)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	return out, nil
}

func (e *Engine) loadRunDetail(ctx context.Context, r db.WorkflowRun) (RunDetail, error) {
	steps, err := e.store.ListWorkflowSteps(ctx, r.ID)
	if err != nil {
		return RunDetail{}, err
	}
	wfName := "(deleted workflow)"
	var def Definition
	if wf, err := e.store.GetWorkflow(ctx, r.WorkflowID); err == nil {
		wfName = wf.Name
		def, _ = ParseDefinition(wf.DefinitionJson)
	}
	return RunDetail{
		Run:          r,
		WorkflowName: wfName,
		Definition:   def,
		Steps:        steps,
	}, nil
}

// AnswerStep resolves a waiting ask step and resumes the workflow run (or stops
// it cleanly if ContinueOnChoices rejects the chosen option).
func (e *Engine) AnswerStep(ctx context.Context, runID, stepID, userID, choice, reason string) (RunDetail, error) {
	choice = strings.TrimSpace(choice)
	reason = strings.TrimSpace(reason)
	if choice == "" {
		return RunDetail{}, fmt.Errorf("%w: choice is required", ErrInvalidAnswer)
	}
	r, err := e.store.GetWorkflowRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if r.OwnerID != userID {
		return RunDetail{}, ErrNotFound
	}
	if r.Status != RunNeedsInput {
		return RunDetail{}, fmt.Errorf("%w: run is %s, not waiting for input", ErrConflict, r.Status)
	}
	step, err := e.store.GetWorkflowStep(ctx, stepID)
	if errors.Is(err, sql.ErrNoRows) || step.RunID != runID {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if step.Status != StepWaiting || step.Kind != StepKindAsk {
		return RunDetail{}, fmt.Errorf("%w: step is %s, not waiting for an answer", ErrConflict, step.Status)
	}

	var choices []string
	_ = json.Unmarshal([]byte(step.AskChoicesJson), &choices)
	if len(choices) > 0 && !slices.Contains(choices, choice) {
		return RunDetail{}, fmt.Errorf("%w: %q is not one of the allowed choices", ErrInvalidAnswer, choice)
	}

	ansBytes, _ := json.Marshal(StepAnswer{Choice: choice, Reason: reason})
	nowUnix := e.now().Unix()
	output := choice
	if reason != "" {
		output = choice + ": " + reason
	}
	step.Status = StepCompleted
	step.AnswerJson = string(ansBytes)
	step.AnsweredBy = userID
	step.Output = output
	step.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
	if err := e.updateStep(ctx, step); err != nil {
		return RunDetail{}, err
	}

	// Check whether the workflow definition restricts which choices continue.
	shouldStop := false
	if wf, err := e.store.GetWorkflow(ctx, r.WorkflowID); err == nil {
		if def, derr := ParseDefinition(wf.DefinitionJson); derr == nil {
			idx := int(step.StepIndex)
			if idx >= 0 && idx < len(def.Steps) {
				allowed := def.Steps[idx].ContinueOnChoices
				if len(allowed) > 0 && !slices.Contains(allowed, choice) {
					shouldStop = true
				}
			}
		}
	}

	if shouldStop {
		steps, _ := e.store.ListWorkflowSteps(ctx, runID)
		for _, s := range steps {
			if s.StepIndex > step.StepIndex && s.Status == StepPending {
				s.Status = StepSkipped
				_ = e.updateStep(ctx, s)
			}
		}
		r.Status = RunCancelled
		r.Error = "Stopped by choice: " + choice
		r.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
		r.UpdatedAt = nowUnix
		if err := e.updateRun(ctx, r); err != nil {
			return RunDetail{}, err
		}
		e.promoteQueued(r.WorkflowID)
		return e.loadRunDetail(ctx, r)
	}

	r.Status = RunRunning
	r.CurrentStep = step.StepIndex + 1
	r.UpdatedAt = nowUnix
	if err := e.updateRun(ctx, r); err != nil {
		return RunDetail{}, err
	}
	e.launchRun(r.ID)
	return e.loadRunDetail(ctx, r)
}

// CancelRun cancels a queued, running, or waiting run and promotes the next
// queued run for that workflow if a concurrency slot opened.
func (e *Engine) CancelRun(ctx context.Context, runID, userID string) (RunDetail, error) {
	r, err := e.store.GetWorkflowRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if r.OwnerID != userID {
		return RunDetail{}, ErrNotFound
	}
	if r.Status == RunCompleted || r.Status == RunFailed || r.Status == RunCancelled {
		return RunDetail{}, fmt.Errorf("%w: run is already %s", ErrConflict, r.Status)
	}

	nowUnix := e.now().Unix()
	r.Status = RunCancelled
	r.Error = "cancelled"
	r.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
	r.UpdatedAt = nowUnix
	if err := e.updateRun(ctx, r); err != nil {
		return RunDetail{}, err
	}

	e.mu.Lock()
	cancelFn := e.cancels[runID]
	e.mu.Unlock()
	if cancelFn != nil {
		cancelFn()
	}

	steps, err := e.store.ListWorkflowSteps(ctx, runID)
	if err == nil {
		for _, s := range steps {
			if s.Status == StepRunning && s.Kind == StepKindAgent && s.SessionID != "" && e.agents != nil {
				_ = e.agents.CancelSession(ctx, s.SessionID)
			}
			if s.Status == StepPending || s.Status == StepRunning || s.Status == StepWaiting {
				s.Status = StepSkipped
				s.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
				_ = e.updateStep(ctx, s)
			}
		}
	}

	e.promoteQueued(r.WorkflowID)
	return e.loadRunDetail(ctx, r)
}

// ResumeRun restarts a failed run from its failed/pending step, respecting the
// workflow's concurrency limit.
func (e *Engine) ResumeRun(ctx context.Context, runID, userID string) (RunDetail, error) {
	r, err := e.store.GetWorkflowRun(ctx, runID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	if r.OwnerID != userID {
		return RunDetail{}, ErrNotFound
	}
	if r.Status != RunFailed {
		return RunDetail{}, fmt.Errorf("%w: only failed runs can be resumed (current status: %s)", ErrConflict, r.Status)
	}
	wf, err := e.store.GetWorkflow(ctx, r.WorkflowID)
	if errors.Is(err, sql.ErrNoRows) {
		return RunDetail{}, ErrNotFound
	}
	if err != nil {
		return RunDetail{}, err
	}
	def, err := ParseDefinition(wf.DefinitionJson)
	if err != nil {
		return RunDetail{}, err
	}

	steps, err := e.store.ListWorkflowSteps(ctx, runID)
	if err != nil {
		return RunDetail{}, err
	}
	for _, s := range steps {
		if s.Status == StepFailed || s.Status == StepRunning {
			s.Status = StepPending
			s.Error = ""
			s.FinishedAt = sql.NullInt64{}
			if err := e.updateStep(ctx, s); err != nil {
				return RunDetail{}, err
			}
		}
	}

	nowUnix := e.now().Unix()
	e.concMu.Lock()
	startNow := true
	if def.ConcurrencyLimit > 0 {
		active, cerr := e.store.CountActiveRunsForWorkflow(ctx, wf.ID)
		if cerr != nil {
			e.concMu.Unlock()
			return RunDetail{}, cerr
		}
		if active >= int64(def.ConcurrencyLimit) {
			startNow = false
		}
	}
	if startNow {
		r.Status = RunRunning
		if !r.StartedAt.Valid {
			r.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
		}
	} else {
		r.Status = RunQueued
	}
	r.Error = ""
	r.FinishedAt = sql.NullInt64{}
	r.UpdatedAt = nowUnix
	err = e.updateRun(ctx, r)
	e.concMu.Unlock()
	if err != nil {
		return RunDetail{}, err
	}

	if startNow {
		e.launchRun(r.ID)
	}
	return e.loadRunDetail(ctx, r)
}

// TriggerWebhook looks up an enabled workflow matching tokenHash and starts a
// run with trigger_kind="webhook".
func (e *Engine) TriggerWebhook(ctx context.Context, tokenHash string, payload map[string]any) (RunDetail, error) {
	if tokenHash == "" {
		return RunDetail{}, ErrNotFound
	}
	wfs, err := e.store.ListEnabledWorkflows(ctx)
	if err != nil {
		return RunDetail{}, err
	}
	for _, wf := range wfs {
		def, derr := ParseDefinition(wf.DefinitionJson)
		if derr != nil || !def.WebhookEnabled || def.WebhookTokenHash == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(def.WebhookTokenHash), []byte(tokenHash)) == 1 {
			return e.StartRun(ctx, wf.ID, wf.OwnerID, TriggerWebhook, payload)
		}
	}
	return RunDetail{}, ErrNotFound
}

// RunScheduler ticks scheduled workflows until ctx is cancelled.
func (e *Engine) RunScheduler(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.TickSchedules(ctx)
		}
	}
}

// TickSchedules checks all enabled workflows with a cron schedule and fires any
// whose NextRunAt is due.
func (e *Engine) TickSchedules(ctx context.Context) {
	now := e.now()
	wfs, err := e.store.ListEnabledWorkflows(ctx)
	if err != nil {
		e.lg.Warn("workflows: list enabled for scheduler", "err", err)
		return
	}
	for _, wf := range wfs {
		def, err := ParseDefinition(wf.DefinitionJson)
		if err != nil || strings.TrimSpace(def.Schedule) == "" {
			continue
		}
		tz := def.Timezone
		if tz == "" {
			tz = "UTC"
		}
		if def.NextRunAt <= 0 {
			next, nerr := trigger.Next(def.Schedule, tz, now)
			if nerr != nil {
				continue
			}
			def.NextRunAt = next.Unix()
			if raw, merr := json.Marshal(def); merr == nil {
				_ = e.store.UpdateWorkflow(ctx, db.UpdateWorkflowParams{
					Name: wf.Name, Description: wf.Description, DefinitionJson: string(raw),
					Enabled: wf.Enabled, UpdatedAt: wf.UpdatedAt, ID: wf.ID,
				})
			}
			continue
		}
		if now.Unix() < def.NextRunAt {
			continue
		}
		next, nerr := trigger.Next(def.Schedule, tz, now)
		if nerr != nil {
			def.NextRunAt = now.Add(100 * 365 * 24 * time.Hour).Unix()
		} else {
			def.NextRunAt = next.Unix()
		}
		if raw, merr := json.Marshal(def); merr == nil {
			_ = e.store.UpdateWorkflow(ctx, db.UpdateWorkflowParams{
				Name: wf.Name, Description: wf.Description, DefinitionJson: string(raw),
				Enabled: wf.Enabled, UpdatedAt: now.Unix(), ID: wf.ID,
			})
		}
		if _, serr := e.StartRun(ctx, wf.ID, wf.OwnerID, TriggerSchedule, map[string]any{
			"scheduledAt": now.Format(time.RFC3339),
		}); serr != nil {
			e.lg.Warn("workflows: scheduled start failed", "workflow", wf.ID, "err", serr)
		}
	}
}

func (e *Engine) launchRun(runID string) {
	e.mu.Lock()
	if e.closing {
		e.mu.Unlock()
		return
	}
	if _, already := e.cancels[runID]; already {
		e.mu.Unlock()
		return
	}
	runCtx, cancel := context.WithCancel(e.ctx)
	e.cancels[runID] = cancel
	e.wg.Add(1)
	e.mu.Unlock()

	go func() {
		defer e.wg.Done()
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.cancels, runID)
			e.mu.Unlock()
		}()
		e.executeRun(runCtx, runID)
	}()
}

func (e *Engine) executeRun(runCtx context.Context, runID string) {
	bg := context.WithoutCancel(runCtx)
	r, err := e.store.GetWorkflowRun(bg, runID)
	if err != nil {
		return
	}
	if r.Status != RunRunning {
		return
	}
	wf, err := e.store.GetWorkflow(bg, r.WorkflowID)
	if err != nil {
		e.failRun(bg, r, "workflow definition no longer exists")
		return
	}
	def, err := ParseDefinition(wf.DefinitionJson)
	if err != nil {
		e.failRun(bg, r, err.Error())
		return
	}
	steps, err := e.store.ListWorkflowSteps(bg, runID)
	if err != nil {
		e.failRun(bg, r, err.Error())
		return
	}
	var input map[string]any
	_ = json.Unmarshal([]byte(r.InputJson), &input)
	if input == nil {
		input = map[string]any{}
	}

	stepOutputs := map[string]string{}
	lastSessionByAgent := map[string]string{}
	lastOutput := ""
	for _, s := range steps {
		if s.Status == StepCompleted {
			stepOutputs[s.Name] = s.Output
			lastOutput = s.Output
			idx := int(s.StepIndex)
			if s.Kind == StepKindAgent && s.SessionID != "" && idx >= 0 && idx < len(def.Steps) {
				lastSessionByAgent[def.Steps[idx].AgentID] = s.SessionID
			}
		}
	}

	for i := range steps {
		if runCtx.Err() != nil {
			return
		}
		step := steps[i]
		if step.Status == StepCompleted || step.Status == StepSkipped {
			continue
		}
		if i >= len(def.Steps) {
			break
		}
		sd := def.Steps[i]

		nowUnix := e.now().Unix()
		r.CurrentStep = int64(i)
		r.UpdatedAt = nowUnix
		if !r.StartedAt.Valid {
			r.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
		}
		_ = e.updateRun(bg, r)

		switch sd.Kind {
		case StepKindAsk:
			title := RenderTemplate(sd.AskTitle, input, stepOutputs)
			choices := sd.AskChoices
			if len(choices) == 0 {
				choices = []string{"Approve", "Reject"}
			}
			choicesJSON, _ := json.Marshal(choices)
			step.Status = StepWaiting
			step.AskTitle = title
			step.AskChoicesJson = string(choicesJSON)
			step.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
			if err := e.updateStep(bg, step); err != nil {
				e.failRun(bg, r, err.Error())
				return
			}
			r.Status = RunNeedsInput
			r.UpdatedAt = nowUnix
			_ = e.updateRun(bg, r)
			return

		case StepKindAgent:
			prompt := RenderTemplate(sd.Prompt, input, stepOutputs)
			step.Status = StepRunning
			step.PromptOrCmd = prompt
			step.Error = ""
			step.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
			_ = e.updateStep(bg, step)

			reuseSID := step.SessionID
			if reuseSID == "" && sd.ReuseSession {
				reuseSID = lastSessionByAgent[sd.AgentID]
			}
			if e.agents == nil {
				e.failStepAndRun(bg, r, step, "agent runner is not configured")
				return
			}
			sid, out, serr := e.agents.RunAgentStep(runCtx, AgentStepRequest{
				RunID:        r.ID,
				WorkflowName: wf.Name,
				StepName:     sd.Name,
				OwnerID:      r.OwnerID,
				AgentID:      sd.AgentID,
				SessionID:    reuseSID,
				Prompt:       prompt,
				OnSession: func(createdSID string) {
					step.SessionID = createdSID
					_ = e.updateStep(bg, step)
				},
			})
			if runCtx.Err() != nil {
				// Respect cancellation or server shutdown (if server shutdown, leave running for startup resume).
				return
			}
			finUnix := e.now().Unix()
			if sid != "" {
				step.SessionID = sid
			}
			if serr != nil {
				step.Output = out
				e.failStepAndRun(bg, r, step, serr.Error())
				return
			}
			step.Status = StepCompleted
			step.Output = out
			step.Error = ""
			step.FinishedAt = sql.NullInt64{Int64: finUnix, Valid: true}
			_ = e.updateStep(bg, step)
			stepOutputs[sd.Name] = out
			lastOutput = out
			if sid != "" {
				lastSessionByAgent[sd.AgentID] = sid
			}

		case StepKindShell:
			cmd := RenderTemplate(sd.Command, input, stepOutputs)
			step.Status = StepRunning
			step.PromptOrCmd = cmd
			step.Error = ""
			step.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
			_ = e.updateStep(bg, step)

			if e.shell == nil {
				e.failStepAndRun(bg, r, step, "shell runner is not configured")
				return
			}
			out, _, serr := e.shell.RunShellStep(runCtx, ShellStepRequest{
				RunID:    r.ID,
				StepName: sd.Name,
				OwnerID:  r.OwnerID,
				AgentID:  sd.AgentID,
				Command:  cmd,
				Cwd:      sd.Cwd,
			})
			if runCtx.Err() != nil {
				return
			}
			finUnix := e.now().Unix()
			step.Output = out
			if serr != nil {
				e.failStepAndRun(bg, r, step, serr.Error())
				return
			}
			step.Status = StepCompleted
			step.Error = ""
			step.FinishedAt = sql.NullInt64{Int64: finUnix, Valid: true}
			_ = e.updateStep(bg, step)
			stepOutputs[sd.Name] = out
			lastOutput = out
		}
	}

	// Re-check run wasn't cancelled right as last step finished.
	if fresh, err := e.store.GetWorkflowRun(bg, r.ID); err == nil && fresh.Status == RunCancelled {
		return
	}
	finUnix := e.now().Unix()
	outJSON, _ := json.Marshal(map[string]any{
		"steps":      stepOutputs,
		"lastOutput": lastOutput,
	})
	r.Status = RunCompleted
	r.OutputJson = string(outJSON)
	r.Error = ""
	r.FinishedAt = sql.NullInt64{Int64: finUnix, Valid: true}
	r.UpdatedAt = finUnix
	_ = e.updateRun(bg, r)
	e.promoteQueued(r.WorkflowID)
}

func (e *Engine) failStepAndRun(ctx context.Context, r db.WorkflowRun, step db.WorkflowStep, msg string) {
	nowUnix := e.now().Unix()
	step.Status = StepFailed
	step.Error = msg
	step.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
	_ = e.updateStep(ctx, step)
	e.failRun(ctx, r, msg)
}

func (e *Engine) failRun(ctx context.Context, r db.WorkflowRun, msg string) {
	if fresh, err := e.store.GetWorkflowRun(ctx, r.ID); err == nil && fresh.Status == RunCancelled {
		return
	}
	nowUnix := e.now().Unix()
	r.Status = RunFailed
	r.Error = msg
	r.FinishedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
	r.UpdatedAt = nowUnix
	_ = e.updateRun(ctx, r)
	e.promoteQueued(r.WorkflowID)
}

func (e *Engine) promoteQueued(workflowID string) {
	e.mu.Lock()
	closing := e.closing
	e.mu.Unlock()
	if closing {
		return
	}
	ctx := e.ctx
	e.concMu.Lock()
	wf, err := e.store.GetWorkflow(ctx, workflowID)
	if err != nil {
		e.concMu.Unlock()
		return
	}
	def, err := ParseDefinition(wf.DefinitionJson)
	if err != nil {
		e.concMu.Unlock()
		return
	}
	queued, err := e.store.ListQueuedRunsForWorkflow(ctx, workflowID)
	if err != nil || len(queued) == 0 {
		e.concMu.Unlock()
		return
	}
	active, err := e.store.CountActiveRunsForWorkflow(ctx, workflowID)
	if err != nil {
		e.concMu.Unlock()
		return
	}
	var toLaunch []string
	nowUnix := e.now().Unix()
	for _, r := range queued {
		if def.ConcurrencyLimit > 0 && active >= int64(def.ConcurrencyLimit) {
			break
		}
		r.Status = RunRunning
		if !r.StartedAt.Valid {
			r.StartedAt = sql.NullInt64{Int64: nowUnix, Valid: true}
		}
		r.UpdatedAt = nowUnix
		if err := e.updateRun(ctx, r); err != nil {
			continue
		}
		active++
		toLaunch = append(toLaunch, r.ID)
	}
	e.concMu.Unlock()

	for _, runID := range toLaunch {
		e.launchRun(runID)
	}
}

func (e *Engine) updateRun(ctx context.Context, r db.WorkflowRun) error {
	return e.store.UpdateWorkflowRunState(ctx, db.UpdateWorkflowRunStateParams{
		Status:      r.Status,
		CurrentStep: r.CurrentStep,
		OutputJson:  r.OutputJson,
		Error:       r.Error,
		StartedAt:   r.StartedAt,
		FinishedAt:  r.FinishedAt,
		UpdatedAt:   r.UpdatedAt,
		ID:          r.ID,
	})
}

func (e *Engine) updateStep(ctx context.Context, s db.WorkflowStep) error {
	return e.store.UpdateWorkflowStep(ctx, db.UpdateWorkflowStepParams{
		Status:         s.Status,
		SessionID:      s.SessionID,
		PromptOrCmd:    s.PromptOrCmd,
		AskTitle:       s.AskTitle,
		AskChoicesJson: s.AskChoicesJson,
		AnswerJson:     s.AnswerJson,
		AnsweredBy:     s.AnsweredBy,
		Output:         s.Output,
		Error:          s.Error,
		StartedAt:      s.StartedAt,
		FinishedAt:     s.FinishedAt,
		ID:             s.ID,
	})
}
