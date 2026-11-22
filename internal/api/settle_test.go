package api_test

import (
	"context"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/session"
)

func auditActions(t *testing.T, c *client, agentID string) []gen.AuditEntry {
	t.Helper()
	var out []gen.AuditEntry
	expect(t, "audit", c.do("GET", "/agents/"+agentID+"/audit", nil, &out), 200)
	return out
}

func countAction(entries []gen.AuditEntry, action string) int {
	n := 0
	for _, e := range entries {
		if e.Action == action {
			n++
		}
	}
	return n
}

func TestSettleSessions(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	ag := tm.agent.Id
	var sess gen.Session
	expect(t, "create", tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{}, &sess), 201)
	if sess.SettledAt != nil || sess.SettledBy != nil {
		t.Fatalf("new session settled: %+v", sess)
	}

	// Whoever may prompt may settle: the owner and shared users, not strangers.
	expect(t, "stranger settles", tm.carol.do("POST", "/sessions/"+sess.Id+"/settle", nil, nil), 404)
	var got gen.Session
	expect(t, "shared user settles", tm.bob.do("POST", "/sessions/"+sess.Id+"/settle", nil, &got), 200)
	if got.SettledAt == nil || got.SettledBy == nil || *got.SettledBy != gen.SessionSettledByUser {
		t.Fatalf("settled: %+v", got)
	}
	expect(t, "settle again", tm.alice.do("POST", "/sessions/"+sess.Id+"/settle", nil, &got), 200)
	var list []gen.Session
	tm.alice.do("GET", "/agents/"+ag+"/sessions", nil, &list)
	if len(list) != 1 || list[0].SettledAt == nil {
		t.Fatalf("list: %+v", list)
	}
	if n := countAction(auditActions(t, tm.alice, ag), audit.SessionSettle); n != 1 {
		t.Fatalf("settles audited: %d, want 1 (settling twice changes nothing)", n)
	}

	expect(t, "stranger unsettles", tm.carol.do("POST", "/sessions/"+sess.Id+"/unsettle", nil, nil), 404)
	got = gen.Session{} // decoding leaves absent fields as they were
	expect(t, "unsettle", tm.bob.do("POST", "/sessions/"+sess.Id+"/unsettle", nil, &got), 200)
	if got.SettledAt != nil || got.SettledBy != nil {
		t.Fatalf("unsettled: %+v", got)
	}
	if n := countAction(auditActions(t, tm.alice, ag), audit.SessionUnsettle); n != 1 {
		t.Fatalf("unsettles audited: %d", n)
	}

	// A new message unsettles a settled thread, and that is audited too.
	expect(t, "settle", tm.alice.do("POST", "/sessions/"+sess.Id+"/settle", nil, nil), 200)
	st := tm.e.stream(tm.alice, sess.Id, 0)
	expect(t, "prompt", tm.alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hello"}, nil), 202)
	st.until(isType("turn_end"))
	got = gen.Session{}
	expect(t, "get", tm.alice.do("GET", "/sessions/"+sess.Id, nil, &got), 200)
	if got.SettledAt != nil {
		t.Fatalf("a prompt left the thread settled: %+v", got)
	}
	entries := auditActions(t, tm.alice, ag)
	if n := countAction(entries, audit.SessionUnsettle); n != 2 {
		t.Fatalf("unsettles audited: %d, want 2", n)
	}
	byActivity := false
	for _, e := range entries {
		byActivity = byActivity || (e.Action == audit.SessionUnsettle && e.Details["reason"] == "activity")
	}
	if !byActivity {
		t.Fatalf("no unsettle by activity in %+v", entries)
	}

	// Auto-settle uses the owner's setting.
	as := &session.AutoSettler{Store: tm.e.store, Audit: &audit.Log{Store: tm.e.store}}
	if n, err := as.Once(context.Background(), time.Now().Add(4*24*time.Hour)); err != nil || n != 1 {
		t.Fatalf("auto-settle: %d %v", n, err)
	}
	expect(t, "get", tm.bob.do("GET", "/sessions/"+sess.Id, nil, &got), 200)
	if got.SettledBy == nil || *got.SettledBy != gen.SessionSettledByAuto {
		t.Fatalf("auto-settled: %+v", got)
	}
}

func TestUserSettings(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	var s gen.UserSettings
	expect(t, "get", alice.do("GET", "/me/settings", nil, &s), 200)
	if s.ThreadAutoSettleDays != session.DefaultAutoSettleDays {
		t.Fatalf("default: %+v", s)
	}
	expect(t, "set", alice.do("PUT", "/me/settings", map[string]int{"threadAutoSettleDays": 0}, &s), 200)
	if s.ThreadAutoSettleDays != 0 {
		t.Fatalf("set: %+v", s)
	}
	expect(t, "empty update", alice.do("PUT", "/me/settings", map[string]any{}, &s), 200)
	if s.ThreadAutoSettleDays != 0 {
		t.Fatalf("empty update changed it: %+v", s)
	}
	expect(t, "negative", alice.do("PUT", "/me/settings", map[string]int{"threadAutoSettleDays": -1}, nil), 400)
	expect(t, "too many", alice.do("PUT", "/me/settings", map[string]int{"threadAutoSettleDays": 366}, nil), 400)
	expect(t, "anonymous", e.anon().do("GET", "/me/settings", nil, nil), 401)
}
