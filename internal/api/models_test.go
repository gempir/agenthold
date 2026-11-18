package api_test

import (
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
)

func (e *env) turnText(c *client, sessionID, prompt string) (string, []gen.SessionEvent) {
	e.t.Helper()
	st := e.stream(c, sessionID, e.lastSeq(c, sessionID))
	expect(e.t, "prompt", c.do("POST", "/sessions/"+sessionID+"/prompt", map[string]string{"text": prompt}, nil), 202)
	evs := st.until(isType("turn_end"))
	return text(evs), evs
}

func (e *env) lastSeq(c *client, sessionID string) int64 {
	var log []gen.SessionEvent
	c.do("GET", "/sessions/"+sessionID+"/events?limit=5000", nil, &log)
	if len(log) == 0 {
		return 0
	}
	return log[len(log)-1].Seq
}

func TestModelPickerFlow(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")

	// Before the harness ran: the adapter's fallback list.
	var cat gen.ModelCatalog
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	if cat.Source != gen.Fallback || !cat.Switchable || len(cat.Models) != 1 || cat.Models[0].Id != "swift-1" || cat.SeenAt != nil {
		t.Fatalf("fallback catalog %+v", cat)
	}
	// Without a reported list anything well-formed is accepted.
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	expect(t, "bad id", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "a b"}, nil), 400)

	if got, _ := e.turnText(alice, sess.Id, "model"); got != "model=swift-1 effort=medium fast=off" {
		t.Fatalf("first turn %q", got)
	}
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	if cat.Source != gen.Harness || !cat.Switchable || cat.SeenAt == nil || len(cat.Models) != 4 {
		t.Fatalf("reported catalog %+v", cat)
	}
	if m := cat.Models[0]; m.Id != "swift-1" || *m.Group != "Fake Labs" || m.Options == nil || len(*m.Options) != 2 {
		t.Fatalf("swift-1 %+v", m)
	}
	if opts := *cat.Models[0].Options; opts[0].Kind != gen.Effort || opts[1].Kind != gen.Toggle || opts[1].Values[1].Value != "on" {
		t.Fatalf("swift-1 options %+v", opts)
	}
	if cat.Models[1].Options != nil {
		t.Fatalf("deep-1 options known before it was used: %+v", *cat.Models[1].Options)
	}

	// Validation against the reported list.
	expect(t, "unknown model", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "gone-0"}, nil), 400)
	expect(t, "bad effort", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"effort": "max"}}, nil), 400)
	expect(t, "no such option", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"turbo": "on"}}, nil), 400)

	// A session choice applies from the next message, without a restart.
	expect(t, "session model", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"effort": "high", "fast": "on"}}, &sess), 200)
	if sess.Model == nil || sess.Model.Model != "swift-1" || (*sess.Model.Options)["fast"] != "on" {
		t.Fatalf("session %+v", sess.Model)
	}
	if got, _ := e.turnText(alice, sess.Id, "model"); got != "model=swift-1 effort=high fast=on" {
		t.Fatalf("after session choice %q", got)
	}

	// The agent default (settings) applies to sessions without a choice.
	var other gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &other), 201)
	expect(t, "agent default", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Model: ptr("deep-1"), ModelOptions: &map[string]string{"effort": "xhigh"}}, &ag), 200)
	if ag.Model != "deep-1" || ag.ModelOptions["effort"] != "xhigh" {
		t.Fatalf("agent %s %v", ag.Model, ag.ModelOptions)
	}
	if got, _ := e.turnText(alice, other.Id, "model"); got != "model=deep-1 effort=xhigh fast=off" {
		t.Fatalf("agent default %q", got)
	}
	if got, _ := e.turnText(alice, sess.Id, "model"); got != "model=swift-1 effort=high fast=on" {
		t.Fatalf("session choice lost %q", got)
	}
	// Back to the agent default.
	var cleared gen.Session
	expect(t, "clear", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: ""}, &cleared), 200)
	if cleared.Model != nil {
		t.Fatalf("session choice after clearing %+v", cleared.Model)
	}
	if got, _ := e.turnText(alice, sess.Id, "model"); got != "model=deep-1 effort=xhigh fast=off" {
		t.Fatalf("after clearing %q", got)
	}

	// A model the harness refuses: a notice, the turn runs on.
	expect(t, "broken", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "broken-1"}, nil), 200)
	got, evs := e.turnText(alice, sess.Id, "model")
	if got != "model=deep-1 effort=xhigh fast=off" {
		t.Fatalf("after refusal %q", got)
	}
	var notice string
	for _, ev := range evs {
		if ev.Type == "notice" {
			notice, _ = ev.Data["message"].(string)
		}
		if ev.Type == "error" {
			t.Fatalf("error event %v", ev.Data)
		}
	}
	if notice == "" || evs[len(evs)-1].Data["stopReason"] != "end_turn" {
		t.Fatalf("notice %q, end %v", notice, evs[len(evs)-1].Data)
	}

	// An old setting the harness no longer lists stays settable for the
	// rest of the settings, and changing the harness resets it.
	expect(t, "rename keeps model", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Name: ptr("renamed")}, &ag), 200)
	if ag.Model != "deep-1" {
		t.Fatalf("model %q after rename", ag.Model)
	}
	var moved gen.Agent
	expect(t, "other harness", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr("claude-code")}, &moved), 200)
	if moved.Model != "" || len(moved.ModelOptions) != 0 {
		t.Fatalf("model %q %v after changing the harness", moved.Model, moved.ModelOptions)
	}
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	if cat.Source != gen.Fallback || cat.Harness != "claude-code" || len(cat.Models) != 4 {
		t.Fatalf("catalog after changing the harness %+v", cat)
	}
}

func TestModelAccess(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	var sess gen.Session
	expect(t, "session", tm.bob.do("POST", "/agents/"+tm.agent.Id+"/sessions", map[string]string{}, &sess), 201)
	expect(t, "shared user reads models", tm.bob.do("GET", "/agents/"+tm.agent.Id+"/models", nil, nil), 200)
	expect(t, "shared user picks the session's model", tm.bob.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1"}, nil), 200)
	expect(t, "shared user cannot set the default", tm.bob.do("PATCH", "/agents/"+tm.agent.Id, gen.AgentPatch{Model: ptr("deep-1")}, nil), 403)
	expect(t, "outsider", tm.carol.do("GET", "/agents/"+tm.agent.Id+"/models", nil, nil), 404)
}
