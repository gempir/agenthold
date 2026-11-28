package platform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store/db"
)

var subthreadTools = []map[string]any{
	{
		"name": "list_harnesses",
		"description": "List the harnesses available on this agenthold installation (for example claude-code, codex, opencode, " +
			"plus any custom harnesses your owner configured) so you can pick one for start_subthread.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name": "start_subthread",
		"description": "Start a sub-thread of your current thread on the SAME agent machine and in the EXACT SAME working " +
			"directory / git worktree so both threads can see and edit the same files. You can run the sub-thread on " +
			"another harness (for example harness=\"codex\" from a Claude Code thread, or vice versa) and optionally pick " +
			"a model. By default wait=true waits for the sub-thread's final answer; pass wait=false to run sub-threads " +
			"in parallel and collect them later with wait_subthread or read_thread.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"text"},
			"properties": map[string]any{
				"text":             map[string]any{"type": "string", "description": "The prompt / task for the sub-thread."},
				"title":            map[string]any{"type": "string", "description": "Optional short title shown in the UI (up to 200 characters)."},
				"harness":          map[string]any{"type": "string", "description": "Harness id or name (see list_harnesses, e.g. \"codex\", \"claude-code\", \"opencode\"). Defaults to your current harness."},
				"model":            map[string]any{"type": "string", "description": "Optional model id for the chosen harness (defaults to that harness's default model)."},
				"reasoning_effort": map[string]any{"type": "string", "description": "Optional reasoning effort level (e.g. \"low\", \"medium\", \"high\")."},
				"wait":             map[string]any{"type": "boolean", "description": "Wait for the sub-thread to finish and return its final answer (default true). Set false to run in the background."},
			},
		},
	},
	{
		"name":        "message_subthread",
		"description": "Send a follow-up message to an existing sub-thread on your machine.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"thread_id", "text"},
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "The sub-thread's id returned by start_subthread."},
				"text":      map[string]any{"type": "string", "description": "The follow-up prompt."},
				"wait":      map[string]any{"type": "boolean", "description": "Wait for the final answer (default true)."},
			},
		},
	},
	{
		"name":        "wait_subthread",
		"description": "Wait for a running sub-thread (started with wait=false) to finish and return its final answer.",
		"inputSchema": map[string]any{
			"type":     "object",
			"required": []string{"thread_id"},
			"properties": map[string]any{
				"thread_id": map[string]any{"type": "string", "description": "The sub-thread's id returned by start_subthread."},
			},
		},
	},
}

func isSubthreadTool(name string) bool {
	switch name {
	case "list_harnesses", "start_subthread", "message_subthread", "wait_subthread":
		return true
	}
	return false
}

type subthreadArgs struct {
	ThreadID        string `json:"thread_id"`
	Text            string `json:"text"`
	Title           string `json:"title"`
	Harness         string `json:"harness"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort"`
	Wait            *bool  `json:"wait"`
}

func (p *Service) subthreadCall(ctx context.Context, caller db.Agent, sessionID, tool string, raw json.RawMessage) (any, error) {
	var in subthreadArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, errors.New("invalid arguments")
		}
	}
	switch tool {
	case "list_harnesses":
		return p.listHarnesses(ctx, caller), nil
	case "start_subthread":
		return p.startSubthread(ctx, caller, sessionID, in)
	case "message_subthread":
		return p.messageSubthread(ctx, caller, sessionID, in)
	case "wait_subthread":
		return p.waitSubthread(ctx, caller, sessionID, in.ThreadID)
	}
	return nil, fmt.Errorf("unknown tool %q", tool)
}

type harnessInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Default bool   `json:"default,omitempty"`
}

func (p *Service) listHarnesses(ctx context.Context, caller db.Agent) string {
	out := []harnessInfo{}
	for _, h := range harness.List(false) {
		out = append(out, harnessInfo{ID: h.ID, Name: h.Name, Default: h.ID == caller.Harness})
	}
	if custom, err := p.Store.ListCustomHarnessesByOwner(ctx, caller.OwnerID); err == nil {
		for _, c := range custom {
			out = append(out, harnessInfo{ID: c.ID, Name: c.Name, Default: c.ID == caller.Harness})
		}
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return string(b)
}

// resolveHarness maps a harness id or display name to its canonical id.
func (p *Service) resolveHarness(ctx context.Context, ownerID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", nil
	}
	if ad, ok := harness.Get(ref); ok {
		return ad.ID, nil
	}
	for _, h := range harness.List(false) {
		if strings.EqualFold(h.ID, ref) || strings.EqualFold(h.Name, ref) {
			return h.ID, nil
		}
	}
	if custom, err := p.Store.ListCustomHarnessesByOwner(ctx, ownerID); err == nil {
		for _, c := range custom {
			if c.ID == ref || strings.EqualFold(c.Name, ref) {
				return c.ID, nil
			}
		}
	}
	var ids []string
	for _, h := range harness.List(false) {
		ids = append(ids, h.ID)
	}
	return "", fmt.Errorf("unknown harness %q (available: %s)", ref, strings.Join(ids, ", "))
}

func (p *Service) startSubthread(ctx context.Context, caller db.Agent, parentSessionID string, in subthreadArgs) (string, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return "", errors.New("text is required")
	}
	depth := p.Sessions.SessionDepth(caller.ID, parentSessionID) + 1
	if depth > MaxDepth {
		p.logger().Warn("subthread stopped by the depth limit", "agent", caller.ID, "parent", parentSessionID, "depth", depth)
		return "", ErrDepth
	}
	resolvedHarness, err := p.resolveHarness(ctx, caller.OwnerID, in.Harness)
	if err != nil {
		return "", err
	}
	effectiveHarness := caller.Harness
	pinnedHarness := ""
	if resolvedHarness != "" {
		effectiveHarness = resolvedHarness
		if resolvedHarness != caller.Harness {
			pinnedHarness = resolvedHarness
		}
	}

	// Inherit the parent thread's exact worktree / working directory and git
	// metadata so the sub-thread works directly alongside the parent thread.
	opts := session.CreateOpts{Harness: pinnedHarness}
	if parentSessionID != "" {
		if parent, err := p.Store.GetAgentSession(ctx, parentSessionID); err == nil && parent.AgentID == caller.ID {
			opts.Cwd = parent.Cwd
			opts.GitProject = parent.GitProject
			opts.GitBranch = parent.GitBranch
			opts.GitRepoPath = parent.GitRepoPath
			if parent.ProjectID != "" {
				if opts.GitBranch == "" {
					opts.GitBranch = parent.ProjectBranch
				}
				if opts.GitProject == "" {
					if proj, perr := p.Store.GetProject(ctx, parent.ProjectID); perr == nil {
						opts.GitProject = proj.Name
					}
				}
			}
		}
	}

	title := strings.TrimSpace(in.Title)
	if len(title) > 200 {
		title = string([]rune(title)[:200])
	}
	child, err := p.Sessions.CreateWith(ctx, caller.ID, caller.OwnerID, title, opts)
	if err != nil {
		return "", err
	}
	if err := p.Store.SetSessionOrigin(ctx, db.SetSessionOriginParams{
		OriginAgentID:   caller.ID,
		OriginSessionID: parentSessionID,
		ID:              child.ID,
	}); err != nil {
		return "", err
	}
	model := strings.TrimSpace(in.Model)
	effort := strings.TrimSpace(in.ReasoningEffort)
	if model != "" || effort != "" {
		c := session.Choice{Model: model, Options: map[string]string{}}
		if effort != "" {
			c.Options["effort"] = effort
		}
		if err := p.Sessions.SetSessionChoice(ctx, child.ID, c); err != nil {
			return "", err
		}
	}
	owner, err := p.Store.GetUser(ctx, caller.OwnerID)
	if err != nil {
		return "", err
	}
	displayTitle := child.Title
	if session.IsPlaceholderTitle(displayTitle) {
		displayTitle = truncate(strings.Join(strings.Fields(text), " "), 60)
	}
	emitStatus := func(status, summary string) {
		if parentSessionID == "" {
			return
		}
		ev := map[string]any{
			"sessionId": child.ID,
			"agentId":   caller.ID,
			"title":     displayTitle,
			"harness":   effectiveHarness,
			"status":    status,
		}
		if model != "" {
			ev["model"] = model
		}
		if child.Cwd != "" {
			ev["cwd"] = child.Cwd
		}
		if child.GitBranch != "" {
			ev["branch"] = child.GitBranch
		}
		if summary != "" {
			ev["summary"] = truncate(summary, 280)
		}
		_ = p.Sessions.RecordSubthread(context.WithoutCancel(ctx), parentSessionID, ev)
	}
	emitStatus("running", "")

	popts := session.PromptOpts{
		Depth:  depth,
		Origin: map[string]string{"agentId": caller.ID, "agentName": caller.Name, "sessionId": parentSessionID},
	}
	sender := "sub-thread from " + caller.Name + " (for " + owner.Name + ")"
	wait := in.Wait == nil || *in.Wait
	p.Audit.Record(ctx, audit.Entry{
		AgentID: caller.ID, UserID: caller.OwnerID, Action: audit.Prompt, Target: child.ID,
		Details: map[string]any{
			"text": truncate(text, 500), "subthread": true, "parentSession": parentSessionID,
			"harness": effectiveHarness, "cwd": child.Cwd, "depth": depth, "wait": wait,
		},
	})

	if !wait {
		if err := p.Sessions.PromptWith(ctx, child.ID, caller.OwnerID, sender, text, nil, popts); err != nil {
			emitStatus("failed", err.Error())
			return "", err
		}
		b, _ := json.MarshalIndent(map[string]any{
			"thread_id": child.ID,
			"title":     displayTitle,
			"harness":   effectiveHarness,
			"cwd":       child.Cwd,
			"status":    "running",
		}, "", "  ")
		return string(b), nil
	}

	timeout := p.AskTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := p.Sessions.Ask(actx, child.ID, caller.OwnerID, sender, text, popts)
	if err != nil {
		emitStatus("failed", err.Error())
		return "", fmt.Errorf("sub-thread %s (%s) failed: %w", child.ID, effectiveHarness, err)
	}
	if answer == "" {
		answer = "(no answer text)"
	}
	emitStatus("completed", answer)
	b, _ := json.MarshalIndent(map[string]any{
		"thread_id": child.ID,
		"title":     displayTitle,
		"harness":   effectiveHarness,
		"cwd":       child.Cwd,
		"status":    "completed",
		"answer":    answer,
	}, "", "  ")
	return string(b), nil
}

func (p *Service) messageSubthread(ctx context.Context, caller db.Agent, parentSessionID string, in subthreadArgs) (string, error) {
	if strings.TrimSpace(in.ThreadID) == "" {
		return "", errors.New("thread_id is required")
	}
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return "", errors.New("text is required")
	}
	child, err := p.ownThread(ctx, caller, in.ThreadID, "")
	if err != nil {
		return "", err
	}
	if child.ID == parentSessionID {
		return "", errors.New("cannot message the current thread as a sub-thread")
	}
	depth := p.Sessions.SessionDepth(caller.ID, parentSessionID) + 1
	if depth > MaxDepth {
		return "", ErrDepth
	}
	owner, err := p.Store.GetUser(ctx, caller.OwnerID)
	if err != nil {
		return "", err
	}
	effectiveHarness := child.Harness
	if effectiveHarness == "" {
		effectiveHarness = caller.Harness
	}
	emitStatus := func(status, summary string) {
		if parentSessionID == "" {
			return
		}
		ev := map[string]any{
			"sessionId": child.ID,
			"agentId":   caller.ID,
			"title":     child.Title,
			"harness":   effectiveHarness,
			"status":    status,
		}
		if child.Cwd != "" {
			ev["cwd"] = child.Cwd
		}
		if child.GitBranch != "" {
			ev["branch"] = child.GitBranch
		}
		if summary != "" {
			ev["summary"] = truncate(summary, 280)
		}
		_ = p.Sessions.RecordSubthread(context.WithoutCancel(ctx), parentSessionID, ev)
	}
	emitStatus("running", "")

	popts := session.PromptOpts{
		Depth:  depth,
		Origin: map[string]string{"agentId": caller.ID, "agentName": caller.Name, "sessionId": parentSessionID},
	}
	sender := "sub-thread from " + caller.Name + " (for " + owner.Name + ")"
	wait := in.Wait == nil || *in.Wait
	if !wait {
		if err := p.Sessions.PromptWith(ctx, child.ID, caller.OwnerID, sender, text, nil, popts); err != nil {
			emitStatus("failed", err.Error())
			return "", err
		}
		b, _ := json.MarshalIndent(map[string]any{
			"thread_id": child.ID,
			"title":     child.Title,
			"harness":   effectiveHarness,
			"status":    "running",
		}, "", "  ")
		return string(b), nil
	}
	timeout := p.AskTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := p.Sessions.Ask(actx, child.ID, caller.OwnerID, sender, text, popts)
	if err != nil {
		emitStatus("failed", err.Error())
		return "", fmt.Errorf("sub-thread %s failed: %w", child.ID, err)
	}
	if answer == "" {
		answer = "(no answer text)"
	}
	emitStatus("completed", answer)
	b, _ := json.MarshalIndent(map[string]any{
		"thread_id": child.ID,
		"title":     child.Title,
		"harness":   effectiveHarness,
		"status":    "completed",
		"answer":    answer,
	}, "", "  ")
	return string(b), nil
}

func (p *Service) waitSubthread(ctx context.Context, caller db.Agent, parentSessionID, threadID string) (string, error) {
	if strings.TrimSpace(threadID) == "" {
		return "", errors.New("thread_id is required")
	}
	child, err := p.ownThread(ctx, caller, threadID, "")
	if err != nil {
		return "", err
	}
	effectiveHarness := child.Harness
	if effectiveHarness == "" {
		effectiveHarness = caller.Harness
	}
	timeout := p.AskTimeout
	if timeout == 0 {
		timeout = 30 * time.Minute
	}
	actx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	answer, err := p.Sessions.WaitTurn(actx, child.ID)
	emit := func(status, summary string) {
		if parentSessionID == "" {
			return
		}
		ev := map[string]any{
			"sessionId": child.ID,
			"agentId":   caller.ID,
			"title":     child.Title,
			"harness":   effectiveHarness,
			"status":    status,
		}
		if summary != "" {
			ev["summary"] = truncate(summary, 280)
		}
		_ = p.Sessions.RecordSubthread(context.WithoutCancel(ctx), parentSessionID, ev)
	}
	if err != nil {
		emit("failed", err.Error())
		return "", fmt.Errorf("sub-thread %s failed: %w", child.ID, err)
	}
	if answer == "" {
		answer = "(no answer text)"
	}
	emit("completed", answer)
	b, _ := json.MarshalIndent(map[string]any{
		"thread_id": child.ID,
		"title":     child.Title,
		"harness":   effectiveHarness,
		"status":    "completed",
		"answer":    answer,
	}, "", "  ")
	return string(b), nil
}
