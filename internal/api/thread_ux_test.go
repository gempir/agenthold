package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/store/db"
)

func TestPerThreadHarnessAndSettingsWithoutMutatingAgent(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	// Create session with custom harness and permissionMode.
	var s1 gen.Session
	expect(t, "create session with harness+perm", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{
		"title":          "Codex thread",
		"harness":        "codex",
		"permissionMode": "supervised",
	}, &s1), 201)
	if s1.Harness == nil || *s1.Harness != "codex" {
		t.Fatalf("s1.Harness = %v, want codex", s1.Harness)
	}
	if s1.PermissionMode == nil || *s1.PermissionMode != gen.Supervised {
		t.Fatalf("s1.PermissionMode = %v, want supervised", s1.PermissionMode)
	}
	if s1.QueuedCount == nil || *s1.QueuedCount != 0 {
		t.Fatalf("s1.QueuedCount = %v, want 0", s1.QueuedCount)
	}
	if s1.ParentKind == nil || *s1.ParentKind != "" {
		t.Fatalf("s1.ParentKind = %v, want empty", s1.ParentKind)
	}
	// Parent agent harness must NOT be mutated!
	if got := alice.agent(ag.Id); got.Harness != "fake" {
		t.Fatalf("parent agent harness mutated to %q, want fake", got.Harness)
	}

	// Switch harness mid-thread via PATCH /sessions/{id}/harness, PUT /model, and PATCH /settings.
	var s2 gen.Session
	expect(t, "create default session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{"title": "Main"}, &s2), 201)
	if s2.Harness == nil || *s2.Harness != "fake" {
		t.Fatalf("s2 default harness = %v, want fake", s2.Harness)
	}
	if _, err := e.turnText(alice, s2.Id, "hello on fake"); len(err) == 0 {
		t.Fatal("expected events from turn")
	}

	var switched gen.Session
	expect(t, "patch harness", alice.do("PATCH", "/sessions/"+s2.Id+"/harness", map[string]any{"harness": "claude-code"}, &switched), 200)
	if switched.Harness == nil || *switched.Harness != "claude-code" {
		t.Fatalf("switched harness = %v, want claude-code", switched.Harness)
	}
	if got := alice.agent(ag.Id); got.Harness != "fake" {
		t.Fatalf("parent agent harness mutated after PATCH /harness: %q", got.Harness)
	}

	// Switch via PUT /sessions/{id}/model with optional harness.
	expect(t, "put model+harness", alice.do("PUT", "/sessions/"+s2.Id+"/model", map[string]any{
		"harness": "codex",
		"model":   "gpt-5",
	}, &switched), 200)
	if switched.Harness == nil || *switched.Harness != "codex" {
		t.Fatalf("PUT model harness = %v, want codex", switched.Harness)
	}
	if switched.Model == nil || switched.Model.Model != "gpt-5" {
		t.Fatalf("PUT model = %+v, want gpt-5", switched.Model)
	}

	// Update permissionMode via PATCH /sessions/{id}/settings.
	expect(t, "patch settings", alice.do("PATCH", "/sessions/"+s2.Id+"/settings", map[string]any{
		"permissionMode": "plan",
		"harness":        "fake",
	}, &switched), 200)
	if switched.PermissionMode == nil || *switched.PermissionMode != gen.Plan {
		t.Fatalf("permissionMode = %v, want plan", switched.PermissionMode)
	}
	if switched.Harness == nil || *switched.Harness != "fake" {
		t.Fatalf("reverted harness = %v, want fake", switched.Harness)
	}

	// Switching harness while running returns 409 Conflict.
	expect(t, "restore full-access", alice.do("PATCH", "/sessions/"+s2.Id+"/settings", map[string]any{
		"permissionMode": "full-access",
	}, nil), 200)
	st := e.stream(alice, s2.Id, e.lastSeq(alice, s2.Id))
	expect(t, "slow turn", alice.do("POST", "/sessions/"+s2.Id+"/prompt", map[string]any{"text": "slow 20"}, nil), 202)
	st.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })
	expect(t, "harness busy 409", alice.do("PATCH", "/sessions/"+s2.Id+"/harness", map[string]any{"harness": "codex"}, nil), 409)
	expect(t, "cancel slow", alice.do("POST", "/sessions/"+s2.Id+"/cancel", nil, nil), 202)
	st.until(isType("turn_end"))

	// ListSessions returns all new metadata fields.
	var list []gen.Session
	expect(t, "list sessions", alice.do("GET", "/agents/"+ag.Id+"/sessions", nil, &list), 200)
	if len(list) != 2 {
		t.Fatalf("list len = %d, want 2", len(list))
	}
	for _, s := range list {
		if s.Harness == nil || s.PermissionMode == nil || s.QueuedCount == nil || s.ParentKind == nil {
			t.Fatalf("session missing metadata fields: %+v", s)
		}
	}
}

func TestQueueEndpointsAndWebSocketBroadcast(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	var sess gen.Session
	expect(t, "create session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]any{}, &sess), 201)

	// Enqueue while idle starts the turn immediately.
	var idleRes gen.EnqueueMessageResult
	expect(t, "enqueue idle", alice.do("POST", "/sessions/"+sess.Id+"/queue", map[string]any{"text": "immediate"}, &idleRes), 201)
	if !idleRes.Started || idleRes.Message != nil {
		t.Fatalf("expected immediate start, got %+v", idleRes)
	}
	st := e.stream(alice, sess.Id, 0)
	st.until(isType("turn_end"))

	// Start a slow turn, then exercise queue CRUD + send-now.
	expect(t, "slow turn", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]any{"text": "slow 30"}, nil), 202)
	st.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })

	var q1, q2 gen.EnqueueMessageResult
	expect(t, "enqueue 1", alice.do("POST", "/sessions/"+sess.Id+"/queue", map[string]any{"text": "first queued"}, &q1), 201)
	expect(t, "enqueue 2", alice.do("POST", "/sessions/"+sess.Id+"/queue", map[string]any{"text": "second queued"}, &q2), 201)
	if q1.Started || q1.Message == nil || q2.Started || q2.Message == nil {
		t.Fatalf("expected queued messages: q1=%+v q2=%+v", q1, q2)
	}

	var gotSess gen.Session
	expect(t, "get session queuedCount", alice.do("GET", "/sessions/"+sess.Id, nil, &gotSess), 200)
	if gotSess.QueuedCount == nil || *gotSess.QueuedCount != 2 {
		t.Fatalf("queuedCount = %v, want 2", gotSess.QueuedCount)
	}

	var listed []gen.QueuedMessage
	expect(t, "list queue", alice.do("GET", "/sessions/"+sess.Id+"/queue", nil, &listed), 200)
	if len(listed) != 2 || listed[0].Id != q1.Message.Id || listed[1].Id != q2.Message.Id {
		t.Fatalf("list queue = %+v", listed)
	}

	// Edit q2 text.
	var edited gen.QueuedMessage
	expect(t, "edit queued", alice.do("PATCH", "/sessions/"+sess.Id+"/queue/"+q2.Message.Id, map[string]any{"text": "second edited"}, &edited), 200)
	if edited.Text != "second edited" {
		t.Fatalf("edited text = %q", edited.Text)
	}

	// Delete q1.
	expect(t, "delete queued", alice.do("DELETE", "/sessions/"+sess.Id+"/queue/"+q1.Message.Id, nil, nil), 204)

	// Send q2 now (steers the running slow turn).
	var sendRes gen.SendQueuedMessageResult
	expect(t, "send-now", alice.do("POST", "/sessions/"+sess.Id+"/queue/"+q2.Message.Id+"/send-now", nil, &sendRes), 202)
	if !sendRes.Steered {
		t.Fatalf("expected steered=true, got %+v", sendRes)
	}

	// Wait for cancelled turn_end then steered turn_end.
	st.until(isType("turn_end"))
	evs := st.until(isType("turn_end"))
	if !strings.Contains(text(evs), "echo: second edited") {
		t.Fatalf("steered turn output = %q", text(evs))
	}
}

func TestRewindSideChatsPinSnoozeAndSearch(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	bob := e.login("bob", "Bob", "b@example.com")
	carol := e.login("carol", "Carol", "c@example.com")

	agAlice := e.fakeAgent(alice, "")
	agBob := e.fakeAgent(bob, "")
	expect(t, "share with carol", alice.do("POST", "/agents/"+agAlice.Id+"/shares", map[string]any{"email": "c@example.com"}, nil), 200)

	// Create parent session and run 2 turns.
	var sess gen.Session
	expect(t, "create session", alice.do("POST", "/agents/"+agAlice.Id+"/sessions", map[string]any{"title": "Billing refactor"}, &sess), 201)
	if err := e.store.SetSessionGit(context.Background(), db.SetSessionGitParams{
		Cwd: "repo", GitProject: "payments", GitBranch: "feat/oauth-pkce", GitRepoPath: "repo", ID: sess.Id,
	}); err != nil {
		t.Fatal(err)
	}
	e.turnText(alice, sess.Id, "remember quasar-nebula-99")
	e.turnText(alice, sess.Id, "wrong turn to rewind")

	// Bob also has a private session with the same token; Alice must never see it.
	var bobSess gen.Session
	expect(t, "bob session", bob.do("POST", "/agents/"+agBob.Id+"/sessions", map[string]any{"title": "Bob private"}, &bobSess), 201)
	e.turnText(bob, bobSess.Id, "secret quasar-nebula-99")

	// Rewind to turn 2.
	var evs []gen.SessionEvent
	expect(t, "events before rewind", alice.do("GET", "/sessions/"+sess.Id+"/events", nil, &evs), 200)
	var turn2Seq int64
	for _, ev := range evs {
		if ev.Type == "prompt" && ev.Data["text"] == "wrong turn to rewind" {
			turn2Seq = ev.Seq
		}
	}
	if turn2Seq == 0 {
		t.Fatalf("missing turn2 prompt in %+v", evs)
	}

	var rw gen.RewindSessionResult
	expect(t, "rewind", alice.do("POST", "/sessions/"+sess.Id+"/rewind", map[string]any{
		"seq": turn2Seq, "revertFiles": true,
	}, &rw), 200)
	if rw.PromptText == nil || *rw.PromptText != "wrong turn to rewind" || rw.DeletedEvents < 2 || rw.FilesReverted {
		t.Fatalf("rewind result = %+v", rw)
	}
	if rw.RevertNote == nil || *rw.RevertNote == "" {
		t.Fatalf("expected revertNote for non-worktree, got %+v", rw)
	}

	// Side chat seeded from parent session.
	var side gen.Session
	expect(t, "start side chat", alice.do("POST", "/sessions/"+sess.Id+"/side-chats", map[string]any{
		"title": "Quick aside",
		"text":  "side question",
	}, &side), 201)
	if side.ParentKind == nil || *side.ParentKind != gen.SessionParentKind("side_chat") {
		t.Fatalf("side.ParentKind = %v, want side_chat", side.ParentKind)
	}
	if side.Origin == nil || side.Origin.SessionId != sess.Id {
		t.Fatalf("side.Origin = %+v", side.Origin)
	}
	sideSt := e.stream(alice, side.Id, 0)
	sideEvs := sideSt.until(isType("turn_end"))
	if !strings.Contains(text(sideEvs), "quasar-nebula-99") {
		t.Fatalf("side chat did not inherit parent context: %q", text(sideEvs))
	}

	// Subthread via API has parentKind == "subthread".
	var sub gen.Session
	expect(t, "start subthread", alice.do("POST", "/sessions/"+sess.Id+"/subthreads", map[string]any{"title": "Sub"}, &sub), 201)
	if sub.ParentKind == nil || *sub.ParentKind != gen.SessionParentKind("subthread") {
		t.Fatalf("sub.ParentKind = %v, want subthread", sub.ParentKind)
	}

	// Pin / Unpin.
	var pinned gen.Session
	expect(t, "pin", alice.do("POST", "/sessions/"+sess.Id+"/pin", nil, &pinned), 200)
	if pinned.PinnedAt == nil {
		t.Fatalf("expected pinnedAt, got %+v", pinned)
	}
	var unpinned gen.Session
	expect(t, "unpin", alice.do("POST", "/sessions/"+sess.Id+"/unpin", nil, &unpinned), 200)
	if unpinned.PinnedAt != nil {
		t.Fatalf("expected nil pinnedAt after unpin, got %+v", unpinned.PinnedAt)
	}

	// Snooze indefinitely, timed, past rejected, unsnooze, and auto-wake on prompt.
	var snoozed gen.Session
	expect(t, "snooze forever", alice.do("POST", "/sessions/"+sess.Id+"/snooze", map[string]any{}, &snoozed), 200)
	if snoozed.Snoozed == nil || !*snoozed.Snoozed || snoozed.SnoozedUntil != nil {
		t.Fatalf("indefinite snooze = %+v", snoozed)
	}
	future := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	var timedSnooze gen.Session
	expect(t, "snooze timed", alice.do("POST", "/sessions/"+sess.Id+"/snooze", map[string]any{"until": future.Format(time.RFC3339)}, &timedSnooze), 200)
	if timedSnooze.Snoozed == nil || !*timedSnooze.Snoozed || timedSnooze.SnoozedUntil == nil || !timedSnooze.SnoozedUntil.Equal(future) {
		t.Fatalf("timed snooze = %+v (want %v)", timedSnooze, future)
	}
	past := time.Now().Add(-time.Hour).UTC()
	expect(t, "snooze past 400", alice.do("POST", "/sessions/"+sess.Id+"/snooze", map[string]any{"until": past.Format(time.RFC3339)}, nil), 400)
	var unsnoozed gen.Session
	expect(t, "unsnooze", alice.do("POST", "/sessions/"+sess.Id+"/unsnooze", nil, &unsnoozed), 200)
	if unsnoozed.Snoozed != nil || unsnoozed.SnoozedUntil != nil {
		t.Fatalf("after unsnooze = %+v", unsnoozed)
	}
	// Auto-wake on new prompt.
	var reSnoozed gen.Session
	expect(t, "re-snooze", alice.do("POST", "/sessions/"+sess.Id+"/snooze", map[string]any{}, &reSnoozed), 200)
	e.turnText(alice, sess.Id, "wake up")
	var woken gen.Session
	expect(t, "get after wake", alice.do("GET", "/sessions/"+sess.Id, nil, &woken), 200)
	if woken.Snoozed != nil || woken.SnoozedUntil != nil {
		t.Fatalf("new message should auto-wake snooze: %+v", woken)
	}

	// Search across title, branch, and message events with access isolation.
	var results []gen.SessionSearchResult
	expect(t, "search message alice", alice.do("GET", "/sessions/search?q=quasar-nebula-99", nil, &results), 200)
	if len(results) == 0 {
		t.Fatal("expected alice search to find quasar-nebula-99")
	}
	for _, r := range results {
		if r.Session.Id == bobSess.Id {
			t.Fatalf("alice search leaked bob's private session: %+v", r)
		}
		if r.Session.Id == sess.Id && (r.MatchField != gen.SessionSearchResultMatchFieldMessage || r.Snippet == nil || !strings.Contains(*r.Snippet, "quasar-nebula-99")) {
			t.Fatalf("unexpected match for sess: %+v", r)
		}
	}

	var carolBranch []gen.SessionSearchResult
	expect(t, "search branch carol", carol.do("GET", "/sessions/search?q=oauth-pkce", nil, &carolBranch), 200)
	if len(carolBranch) != 3 {
		t.Fatalf("carol branch search len = %d, want 3 (parent + side chat + subthread on same branch): %+v", len(carolBranch), carolBranch)
	}
	foundParentBranch := false
	for _, r := range carolBranch {
		if r.Session.Id == sess.Id && r.MatchField == gen.SessionSearchResultMatchFieldBranch {
			foundParentBranch = true
		}
	}
	if !foundParentBranch {
		t.Fatalf("carol branch search missing parent session: %+v", carolBranch)
	}

	var bobBilling []gen.SessionSearchResult
	expect(t, "search title bob", bob.do("GET", "/sessions/search?q=Billing", nil, &bobBilling), 200)
	if len(bobBilling) != 0 {
		t.Fatalf("bob should not see alice's Billing session: %+v", bobBilling)
	}

	// Verify queue_updated WebSocket frame is emitted when enqueueing while running.
	wsStream := e.stream(alice, sess.Id, e.lastSeq(alice, sess.Id))
	expect(t, "slow for ws queue check", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]any{"text": "slow 20"}, nil), 202)
	wsStream.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })
	expect(t, "enqueue for ws", alice.do("POST", "/sessions/"+sess.Id+"/queue", map[string]any{"text": "ws check"}, nil), 201)
	sawQueueUpdated := false
	readCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for !sawQueueUpdated {
		_, data, err := wsStream.ws.Read(readCtx)
		if err != nil {
			t.Fatalf("ws read waiting for queue_updated: %v", err)
		}
		var frame struct {
			Type  string `json:"type"`
			Count int    `json:"count"`
		}
		if json.Unmarshal(data, &frame) == nil && frame.Type == "queue_updated" && frame.Count == 1 {
			sawQueueUpdated = true
		}
	}
	expect(t, "cancel ws slow", alice.do("POST", "/sessions/"+sess.Id+"/cancel", nil, nil), 202)
}
