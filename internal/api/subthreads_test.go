package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestSubthreadsShareWorktreeAndEmitEvents(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	// Start a parent session in a worktree directory.
	var parent gen.Session
	expect(t, "create parent", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Build feature"}, &parent), 201)
	if err := e.store.SetSessionGit(context.Background(), db.SetSessionGitParams{
		Cwd: "projects/shop/.threads/abc", GitProject: "shop", GitBranch: "agenthold/abc", GitRepoPath: "projects/shop/.threads/abc", ID: parent.Id,
	}); err != nil {
		t.Fatal(err)
	}

	// list_harnesses exposes the built-in harnesses.
	rawHarnesses, err := e.platform.Call(context.Background(), ag.Id, parent.Id, "list_harnesses", nil)
	if err != nil {
		t.Fatalf("list_harnesses: %v", err)
	}
	if hs := rawHarnesses.(string); !strings.Contains(hs, `"claude-code"`) || !strings.Contains(hs, `"codex"`) {
		t.Fatalf("list_harnesses missing built-ins: %s", hs)
	}

	// Synchronous sub-thread (wait=true default) shares parent's worktree and returns its answer.
	args, _ := json.Marshal(map[string]any{
		"title":   "Review diff",
		"harness": "fake",
		"text":    "check the worktree",
	})
	rawRes, err := e.platform.Call(context.Background(), ag.Id, parent.Id, "start_subthread", args)
	if err != nil {
		t.Fatalf("start_subthread: %v", err)
	}
	var res struct {
		ThreadID string `json:"thread_id"`
		Title    string `json:"title"`
		Harness  string `json:"harness"`
		Cwd      string `json:"cwd"`
		Status   string `json:"status"`
		Answer   string `json:"answer"`
	}
	if err := json.Unmarshal([]byte(rawRes.(string)), &res); err != nil {
		t.Fatalf("decode start_subthread result: %v (%s)", err, rawRes)
	}
	if res.ThreadID == "" || res.Status != "completed" || !strings.Contains(res.Answer, "echo: check the worktree") {
		t.Fatalf("unexpected start_subthread result: %+v", res)
	}
	if res.Cwd != "projects/shop/.threads/abc" {
		t.Fatalf("sub-thread cwd = %q, want parent's worktree", res.Cwd)
	}

	// Inspect the child session through the API: same worktree, linked back to parent.
	var child gen.Session
	expect(t, "get child", alice.do("GET", "/sessions/"+res.ThreadID, nil, &child), 200)
	if child.Origin == nil || child.Origin.AgentId != ag.Id || child.Origin.SessionId != parent.Id {
		t.Fatalf("child origin = %+v", child.Origin)
	}
	if child.Origin.SessionTitle == nil || *child.Origin.SessionTitle != "Build feature" {
		t.Fatalf("child origin title = %+v", child.Origin.SessionTitle)
	}
	if child.Cwd == nil || *child.Cwd != "projects/shop/.threads/abc" {
		t.Fatalf("child cwd = %v", child.Cwd)
	}
	if child.Git == nil || child.Git.Project != "shop" || child.Git.Branch != "agenthold/abc" {
		t.Fatalf("child git = %+v", child.Git)
	}

	// Parent session's event log recorded running -> completed subthread events.
	var evs []gen.SessionEvent
	expect(t, "parent events", alice.do("GET", "/sessions/"+parent.Id+"/events", nil, &evs), 200)
	var statuses []string
	for _, ev := range evs {
		if ev.Type == "subthread" && ev.Data["sessionId"] == child.Id {
			statuses = append(statuses, ev.Data["status"].(string))
		}
	}
	if len(statuses) != 2 || statuses[0] != "running" || statuses[1] != "completed" {
		t.Fatalf("parent subthread statuses = %v, want [running completed]", statuses)
	}

	// Follow-up message_subthread works on the same sub-thread.
	msgArgs, _ := json.Marshal(map[string]any{"thread_id": child.Id, "text": "follow up"})
	rawFollow, err := e.platform.Call(context.Background(), ag.Id, parent.Id, "message_subthread", msgArgs)
	if err != nil {
		t.Fatalf("message_subthread: %v", err)
	}
	if !strings.Contains(rawFollow.(string), "echo: follow up") {
		t.Fatalf("message_subthread: %s", rawFollow)
	}

	// Async sub-thread (wait=false) + wait_subthread.
	asyncArgs, _ := json.Marshal(map[string]any{"title": "Background work", "text": "bg task", "wait": false})
	rawAsync, err := e.platform.Call(context.Background(), ag.Id, parent.Id, "start_subthread", asyncArgs)
	if err != nil {
		t.Fatalf("async start_subthread: %v", err)
	}
	var asyncRes struct {
		ThreadID string `json:"thread_id"`
		Status   string `json:"status"`
	}
	_ = json.Unmarshal([]byte(rawAsync.(string)), &asyncRes)
	if asyncRes.Status != "running" || asyncRes.ThreadID == "" {
		t.Fatalf("async result = %+v", asyncRes)
	}
	waitArgs, _ := json.Marshal(map[string]any{"thread_id": asyncRes.ThreadID})
	rawWait, err := e.platform.Call(context.Background(), ag.Id, parent.Id, "wait_subthread", waitArgs)
	if err != nil {
		t.Fatalf("wait_subthread: %v", err)
	}
	if !strings.Contains(rawWait.(string), "echo: bg task") {
		t.Fatalf("wait_subthread: %s", rawWait)
	}
}

func TestCrossHarnessSubthreadAndEgressLogins(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	expect(t, "claude login", alice.connect("claude-code", "oauth-token", "sk-ant-oat01-real-claude-token-abcd"), 200)
	expect(t, "codex login", alice.connect("codex", "api-key", "sk-proj-real-openai-key-wxyz"), 200)

	ag := e.fakeAgent(alice, "")
	expect(t, "switch claude", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr("claude-code")}, nil), 200)

	// Least privilege: before any cross-harness sub-thread, only Claude login is injected.
	if injs := e.injections(ag.Id).Injections; len(injs) != 1 {
		t.Fatalf("before sub-thread: %d injections, want 1", len(injs))
	}

	var parent gen.Session
	expect(t, "create parent", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Opus parent"}, &parent), 201)
	if err := e.store.SetSessionGit(context.Background(), db.SetSessionGitParams{
		Cwd: "projects/app", GitProject: "app", GitBranch: "main", GitRepoPath: "projects/app", ID: parent.Id,
	}); err != nil {
		t.Fatal(err)
	}

	// Start a Codex sub-thread from the Claude Code parent thread via HTTP API.
	var sub gen.Session
	expect(t, "start codex subthread", alice.do("POST", "/sessions/"+parent.Id+"/subthreads", map[string]any{
		"title":   "Codex review",
		"harness": "codex",
	}, &sub), 201)
	if sub.Harness == nil || *sub.Harness != "codex" {
		t.Fatalf("sub harness = %v, want codex", sub.Harness)
	}
	if sub.Cwd == nil || *sub.Cwd != "projects/app" {
		t.Fatalf("sub cwd = %v, want projects/app", sub.Cwd)
	}
	if sub.Origin == nil || sub.Origin.SessionId != parent.Id || sub.Origin.SessionTitle == nil || *sub.Origin.SessionTitle != "Opus parent" {
		t.Fatalf("sub origin = %+v", sub.Origin)
	}

	// Now the egress proxy automatically injects BOTH Claude and Codex logins for this agent.
	a := e.injections(ag.Id)
	if len(a.Injections) != 2 {
		t.Fatalf("with codex sub-thread: %d injections, want 2", len(a.Injections))
	}
}
