package api_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
	expect(t, "options before a report", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"effort": "high"}}, nil), 400)

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
	// The owner may use ids the harness does not list (it decides).
	expect(t, "unlisted model", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "gone-0"}, nil), 200)
	expect(t, "mode is not a model option", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "", Options: &map[string]string{"mode": "bypass"}}, nil), 400)
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
	// Before the harness reported its models, only the owner picks one.
	expect(t, "unreported model", tm.bob.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1"}, nil), 403)
	if got, _ := tm.e.turnText(tm.bob, sess.Id, "model"); got != "model=swift-1 effort=medium fast=off" {
		t.Fatalf("first turn %q", got)
	}
	expect(t, "reported model", tm.bob.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "deep-1"}, nil), 200)
	expect(t, "unlisted model", tm.bob.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "gone-0"}, nil), 403)
	// The bypass attempt: a session mode through the model options.
	expect(t, "mode", tm.bob.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"mode": "bypass"}}, nil), 400)
	if got, _ := tm.e.turnText(tm.bob, sess.Id, "mode"); got != "mode=default" {
		t.Fatalf("mode %q", got)
	}
	expect(t, "shared user cannot set the default", tm.bob.do("PATCH", "/agents/"+tm.agent.Id, gen.AgentPatch{Model: ptr("deep-1")}, nil), 403)
	expect(t, "outsider", tm.carol.do("GET", "/agents/"+tm.agent.Id+"/models", nil, nil), 404)
}

func TestDefaultModelWithoutConfigOptions(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	dir := filepath.Join(e.home(ag.Id), ".fakeagent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "script"), []byte("no-config\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	if got, _ := e.turnText(alice, sess.Id, "model"); !strings.HasPrefix(got, "model=swift-1 ") {
		t.Fatalf("first turn %q", got)
	}
	var cat gen.ModelCatalog
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	if cat.Switchable || cat.SeenAt == nil {
		t.Fatalf("catalog %+v", cat)
	}
	// The harness only reads its model when it starts: the new default
	// restarts it, and the next turn runs on it.
	expect(t, "default", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Model: ptr("deep-1")}, nil), 200)
	if got, _ := e.turnText(alice, sess.Id, "model"); !strings.HasPrefix(got, "model=deep-1 ") {
		t.Fatalf("after the new default %q", got)
	}
	// Clearing it restarts it again, on the harness's own default.
	expect(t, "clear", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Model: ptr("")}, nil), 200)
	if got, _ := e.turnText(alice, sess.Id, "model"); !strings.HasPrefix(got, "model=swift-1 ") {
		t.Fatalf("after clearing %q", got)
	}
}

func TestModelEffortForModelsNotUsedYet(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	e.turnText(alice, sess.Id, "model")

	// The harness has described its current model only: the other models
	// show the levels it reported (provisional), with its own values only.
	var cat gen.ModelCatalog
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	if cat.ProvisionalOptions == nil || len(*cat.ProvisionalOptions) != 1 {
		t.Fatalf("provisional options %+v", cat.ProvisionalOptions)
	}
	prov := (*cat.ProvisionalOptions)[0]
	if prov.Id != "effort" || prov.Kind != gen.Effort || len(prov.Values) != 3 || prov.Values[2].Value != "high" {
		t.Fatalf("provisional effort %+v", prov)
	}
	expect(t, "provisional level", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "deep-1", Options: &map[string]string{"effort": "high"}}, nil), 200)
	// The permission mode is no model option, whatever the model.
	expect(t, "mode", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "deep-1", Options: &map[string]string{"mode": "bypass"}}, nil), 400)

	// Once the agent is idle the harness is asked about every model.
	if err := e.sessions.ProbeModels(t.Context(), ag.Id); err != nil {
		t.Fatal(err)
	}
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	deep := cat.Models[1]
	if deep.Id != "deep-1" || deep.Options == nil || len(*deep.Options) != 1 || len((*deep.Options)[0].Values) != 5 {
		t.Fatalf("deep-1 after the probe %+v", deep)
	}
	expect(t, "exact level", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "deep-1", Options: &map[string]string{"effort": "max"}}, nil), 200)
	expect(t, "level the model lacks", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"effort": "max"}}, nil), 400)
	// The refused choice changed nothing.
	if got, _ := e.turnText(alice, sess.Id, "model"); got != "model=deep-1 effort=max fast=off" {
		t.Fatalf("turn %q", got)
	}
}

func TestUltracodeIsNotAModelOption(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	dir := filepath.Join(e.home(ag.Id), ".fakeagent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Claude Code's fast mode and ultracode are both on/off options of
	// category model_config, listed "on" first.
	if err := os.WriteFile(filepath.Join(dir, "script"), []byte("ultracode\nfast-on-first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	e.turnText(alice, sess.Id, "model")
	var cat gen.ModelCatalog
	expect(t, "models", alice.do("GET", "/agents/"+ag.Id+"/models", nil, &cat), 200)
	opts := *cat.Models[0].Options
	if len(opts) != 2 || opts[0].Kind != gen.Effort || opts[1].Id != "fast" || opts[1].Kind != gen.Toggle {
		t.Fatalf("swift-1 options %+v", opts)
	}
	// Whatever the order the harness lists them in, off comes first for clients.
	if opts[1].Values[0].Value != "off" || opts[1].Values[1].Value != "on" {
		t.Fatalf("fast values %+v", opts[1].Values)
	}
	for _, model := range []string{"swift-1", "deep-1", "gone-0"} {
		expect(t, "ultracode "+model, alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: model, Options: &map[string]string{"ultracode": "on"}}, nil), 400)
	}
	expect(t, "fast", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "swift-1", Options: &map[string]string{"fast": "on"}}, nil), 200)
}

func TestModelCatalogOfAnotherHarness(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	base := "/agents/" + tm.agent.Id + "/models"
	// The owner sees the catalog the agent would have on another harness (the
	// built-in list: nothing of it was reported) and the login it would use.
	var cat gen.ModelCatalog
	expect(t, "codex catalog", tm.alice.do("GET", base+"?harness=codex", nil, &cat), 200)
	if cat.Harness != "codex" || cat.Source != gen.Fallback || cat.Login == nil || cat.Login.Source != "account" || cat.Login.Connected {
		t.Fatalf("codex catalog %+v login %+v", cat, cat.Login)
	}
	expect(t, "connect", tm.alice.connect("codex", "api-key", "sk-proj-real-openai-key-wxyz"), 200)
	expect(t, "codex catalog again", tm.alice.do("GET", base+"?harness=codex", nil, &cat), 200)
	if !cat.Login.Connected {
		t.Fatalf("login after connecting %+v", cat.Login)
	}
	// Its own harness asked for by name is the usual catalog.
	expect(t, "own harness", tm.alice.do("GET", base+"?harness=fake", nil, &cat), 200)
	if cat.Harness != "fake" || len(cat.Models) != 1 {
		t.Fatalf("own catalog %+v", cat)
	}
	expect(t, "unknown harness", tm.alice.do("GET", base+"?harness=nope", nil, nil), 400)
	// Switching is the owner's: others see only the agent's own harness.
	expect(t, "shared user", tm.bob.do("GET", base+"?harness=codex", nil, nil), 403)
	expect(t, "shared user, own harness", tm.bob.do("GET", base+"?harness=fake", nil, nil), 200)
	expect(t, "outsider", tm.carol.do("GET", base+"?harness=codex", nil, nil), 404)
}

func TestSwitchingHarnessResetsConversationModels(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	e.turnText(alice, sess.Id, "model")
	expect(t, "choose", alice.do("PUT", "/sessions/"+sess.Id+"/model", gen.ModelChoice{Model: "deep-1"}, nil), 200)

	// A turn in progress keeps the harness: switching waits.
	// Read the log up to the first turn's end, so the turn_end awaited below
	// is the cancelled one.
	st := e.stream(alice, sess.Id, 0)
	st.until(isType("turn_end"))
	expect(t, "slow prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 50"}, nil), 202)
	expect(t, "switch while busy", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr("codex")}, nil), 409)
	// Cancel once the harness answers (as the cancel test in sessions_test
	// does): the turn then ends promptly as cancelled.
	st.until(func(ev gen.SessionEvent) bool { return strings.Contains(text([]gen.SessionEvent{ev}), "chunk 1") })
	expect(t, "cancel", alice.do("POST", "/sessions/"+sess.Id+"/cancel", nil, nil), 202)
	if end := st.until(isType("turn_end")); end[len(end)-1].Data["stopReason"] != "cancelled" {
		t.Fatalf("after cancel: %v", end[len(end)-1].Data)
	}
	// turn_end is streamed just before the session manager lets go of the
	// agent; switching waits for that (a slow database widens the gap).
	waitUntil(t, "agent idle", 5*time.Second, func() bool { return !e.sessions.AgentBusy(ag.Id) })

	var got gen.Agent
	expect(t, "switch", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr("codex"), Model: ptr("gpt-5")}, &got), 200)
	if got.Harness != "codex" || got.Model != "gpt-5" {
		t.Fatalf("agent after the switch %+v", got)
	}
	var list []gen.Session
	expect(t, "sessions", alice.do("GET", "/agents/"+ag.Id+"/sessions", nil, &list), 200)
	if len(list) != 1 || (list[0].Model != nil && list[0].Model.Model != "") {
		t.Fatalf("conversation model after the switch %+v", list)
	}
}
