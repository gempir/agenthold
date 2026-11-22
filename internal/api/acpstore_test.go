package api_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/harness"
	"github.com/gempir/agenthold/internal/harness/antigravity"
)

func TestACPStoreRegistryAndCustomHarnesses(t *testing.T) {
	e := newEnv(t)

	// Point the ACP Store catalog at a local mock registry server.
	mockReg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"version": "1.0.0",
			"agents": [
				{
					"id": "goose",
					"name": "Goose",
					"version": "1.2.3",
					"description": "Open source AI developer agent",
					"authors": ["Block"],
					"license": "Apache-2.0",
					"website": "https://block.github.io/goose",
					"repository": "https://github.com/block/goose",
					"icon": "https://cdn.agentclientprotocol.com/icons/goose.svg",
					"distribution": {
						"binary": {
							"linux-x86_64": {"archive": "https://example.com/goose-x86_64.tar.gz", "cmd": "./goose"}
						},
						"npx": {"package": "@block/goose-acp@1.2.3"}
					}
				},
				{
					"id": "codex-acp",
					"name": "Codex",
					"version": "0.10.0",
					"description": "OpenAI Codex ACP adapter",
					"distribution": {
						"npx": {"package": "@zed-industries/codex-acp@0.10.0"}
					}
				}
			]
		}`))
	}))
	t.Cleanup(mockReg.Close)
	e.acpStore.URL = mockReg.URL

	alice := e.login("alice", "Alice", "alice@example.com")
	bob := e.login("bob", "Bob", "bob@example.com")

	// 1. Search ACP Registry.
	var searchRes gen.AcpRegistrySearchResponse
	expect(t, "search registry", alice.do("GET", "/acp-registry?q=goose", nil, &searchRes), 200)
	if searchRes.Version != "1.0.0" || len(searchRes.Agents) != 1 || searchRes.Agents[0].Id != "goose" {
		t.Fatalf("unexpected search response: %+v", searchRes)
	}
	if searchRes.Agents[0].AddedId != nil {
		t.Fatalf("expected addedId nil before adding, got %v", *searchRes.Agents[0].AddedId)
	}

	// Empty query returns all agents including builtinId mapping.
	var allRes gen.AcpRegistrySearchResponse
	expect(t, "search all", alice.do("GET", "/acp-registry", nil, &allRes), 200)
	if len(allRes.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(allRes.Agents))
	}
	var sawCodexBuiltin bool
	for _, a := range allRes.Agents {
		if a.Id == "codex-acp" && a.BuiltinId != nil && *a.BuiltinId == "codex" {
			sawCodexBuiltin = true
		}
	}
	if !sawCodexBuiltin {
		t.Fatalf("expected codex-acp builtinId=codex, got %+v", allRes.Agents)
	}

	// 2. Add a registry harness.
	var regHarness gen.CustomHarness
	expect(t, "add registry harness", alice.do("POST", "/me/harnesses", gen.CustomHarnessInput{
		Kind:         gen.CustomHarnessInputKind("registry"),
		RegistryId:   ptr("goose"),
		Distribution: ptr(gen.CustomHarnessInputDistribution("npx")),
		CustomModels: ptr([]string{"goose-pro"}),
		Env:          ptr(map[string]string{"FOO": "bar"}),
	}, &regHarness), 201)
	if !strings.HasPrefix(regHarness.Id, "acp_") || regHarness.Name != "Goose" || regHarness.RegistryVersion == nil || *regHarness.RegistryVersion != "1.2.3" {
		t.Fatalf("unexpected created registry harness: %+v", regHarness)
	}

	// Re-searching registry now reports addedId.
	var afterAdd gen.AcpRegistrySearchResponse
	expect(t, "search after add", alice.do("GET", "/acp-registry?q=goose", nil, &afterAdd), 200)
	if len(afterAdd.Agents) != 1 || afterAdd.Agents[0].AddedId == nil || *afterAdd.Agents[0].AddedId != regHarness.Id {
		t.Fatalf("expected addedId=%s, got %+v", regHarness.Id, afterAdd.Agents)
	}

	// 3. Add a local ACP harness pointing at the compiled fakeagent binary.
	fakeAd, ok := harness.Get("fake")
	if !ok || len(fakeAd.Command) == 0 {
		t.Fatal("fake harness command not configured")
	}
	var localHarness gen.CustomHarness
	expect(t, "add local harness", alice.do("POST", "/me/harnesses", gen.CustomHarnessInput{
		Kind:         gen.CustomHarnessInputKind("local"),
		Name:         ptr("My Local ACP"),
		Description:  ptr("Runs local fakeagent"),
		Command:      ptr(fakeAd.Command[0]),
		CustomModels: ptr([]string{"local-m1"}),
	}, &localHarness), 201)
	if !strings.HasPrefix(localHarness.Id, "loc_") || localHarness.Name != "My Local ACP" {
		t.Fatalf("unexpected local harness: %+v", localHarness)
	}

	// 4. ListHarnesses includes Alice's custom harnesses, not Bob's.
	var aliceHarnesses, bobHarnesses []gen.HarnessType
	expect(t, "alice harnesses", alice.do("GET", "/harnesses", nil, &aliceHarnesses), 200)
	expect(t, "bob harnesses", bob.do("GET", "/harnesses", nil, &bobHarnesses), 200)
	hasID := func(list []gen.HarnessType, id string) bool {
		for _, h := range list {
			if h.Id == id {
				return true
			}
		}
		return false
	}
	if !hasID(aliceHarnesses, localHarness.Id) || !hasID(aliceHarnesses, regHarness.Id) {
		t.Fatalf("alice missing custom harnesses: %+v", aliceHarnesses)
	}
	if hasID(bobHarnesses, localHarness.Id) || hasID(bobHarnesses, regHarness.Id) {
		t.Fatalf("bob should not see alice's custom harnesses: %+v", bobHarnesses)
	}

	// Bob cannot create an agent using Alice's custom harness.
	expect(t, "bob cannot use alice harness", bob.do("POST", "/agents", gen.AgentInput{
		Name:    "bob-agent",
		Harness: ptr(localHarness.Id),
	}, nil), 400)

	// 5. Alice creates an agent on the built-in fake harness, chats, then switches
	// to her local ACP harness and verifies Context Handoff carries the conversation!
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "create session", alice.do("POST", "/agents/"+ag.Id+"/sessions", nil, &sess), 201)
	st := e.stream(alice, sess.Id, 0)
	expect(t, "first prompt", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hello from harness one"}, nil), 202)
	evs := st.until(isType("turn_end"))
	if got := text(evs); !strings.Contains(got, "echo: hello from harness one") {
		t.Fatalf("expected echo reply, got %q", got)
	}

	// Switch the agent to Alice's local ACP harness.
	var updatedAg gen.Agent
	expect(t, "switch to local harness", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{
		Harness: ptr(localHarness.Id),
	}, &updatedAg), 200)
	if updatedAg.Harness != localHarness.Id {
		t.Fatalf("expected harness %s, got %s", localHarness.Id, updatedAg.Harness)
	}

	// Cannot delete a custom harness while an agent uses it.
	expect(t, "delete in-use harness", alice.do("DELETE", "/me/harnesses/"+localHarness.Id, nil, nil), 409)

	// Prompt again on the switched harness: should emit context handoff notice and
	// include the earlier conversation in the prompt sent to the new harness!
	expect(t, "second prompt after switch", alice.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "follow up question"}, nil), 202)
	evs2 := st.until(isType("turn_end"))
	var sawHandoffNotice bool
	for _, ev := range evs2 {
		if ev.Type == "notice" {
			if msg, _ := ev.Data["message"].(string); strings.Contains(msg, "context handoff") {
				sawHandoffNotice = true
			}
		}
	}
	if !sawHandoffNotice {
		t.Fatalf("expected context handoff notice after harness switch, got events: %+v", evs2)
	}
	reply2 := text(evs2)
	if !strings.Contains(reply2, "hello from harness one") || !strings.Contains(reply2, "follow up question") {
		t.Fatalf("expected handed-off context + new prompt in reply, got %q", reply2)
	}

	// 6. Update custom harness, then switch agent back and delete both custom harnesses.
	var patched gen.CustomHarness
	expect(t, "patch local harness", alice.do("PATCH", "/me/harnesses/"+localHarness.Id, gen.CustomHarnessPatch{
		Name: ptr("Renamed Local ACP"),
	}, &patched), 200)
	if patched.Name != "Renamed Local ACP" {
		t.Fatalf("expected renamed harness, got %+v", patched)
	}

	expect(t, "switch back", alice.do("PATCH", "/agents/"+ag.Id, gen.AgentPatch{Harness: ptr("fake")}, nil), 200)
	expect(t, "delete local harness", alice.do("DELETE", "/me/harnesses/"+localHarness.Id, nil, nil), 204)
	expect(t, "delete registry harness", alice.do("DELETE", "/me/harnesses/"+regHarness.Id, nil, nil), 204)
}

func TestAntigravityInteractiveAuthFlow(t *testing.T) {
	e := newEnv(t)

	// Script mimicking `agy acp --login`: prints the Google OAuth URL, reads one
	// line from stdin, and exits 0 only if it matches "valid-code-123".
	script := filepath.Join(t.TempDir(), "fake-agy-login.sh")
	body := `#!/bin/sh
echo "Open the following link to authenticate the ACP server: https://accounts.google.com/o/oauth2/auth?client_id=test&redirect_uri=https%3A%2F%2Fantigravity.google%2Foauth-callback&response_type=code&state=state-xyz"
IFS= read -r line
if [ "$line" = "valid-code-123" ]; then
  echo "Authenticated!"
  exit 0
fi
echo "Invalid code" >&2
exit 1
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	e.api.AntigravityLoginCmd = []string{"/bin/sh", script}

	alice := e.login("alice", "Alice", "alice@example.com")

	var ag gen.Agent
	expect(t, "create antigravity agent", alice.do("POST", "/agents", gen.AgentInput{
		Name:    "agy-bot",
		Harness: ptr("antigravity"),
	}, &ag), 201)
	e.sandboxes.Wait()

	// Start interactive sign-in flow.
	var flow gen.HarnessAuthFlow
	expect(t, "start auth flow", alice.do("POST", "/agents/"+ag.Id+"/harness/auth/start", nil, &flow), 200)
	if flow.Status != gen.HarnessAuthFlowStatusWaitingForCode || flow.FlowId == "" || flow.AuthorizationUrl == nil {
		t.Fatalf("unexpected start response: %+v", flow)
	}
	if _, err := antigravity.ParseAuthorizationURL(*flow.AuthorizationUrl); err != nil {
		t.Fatalf("invalid authorization URL returned: %v", err)
	}

	// Submitting a malformed code returns 400 and keeps the flow alive.
	expect(t, "bad code format", alice.do("POST", "/agents/"+ag.Id+"/harness/auth/submit", gen.SubmitHarnessAuthJSONBody{
		FlowId: flow.FlowId,
		Code:   "not a valid code with spaces",
	}, nil), 400)

	// Submitting the valid code finishes sign-in and marks credentialsInSandbox.
	var doneFlow gen.HarnessAuthFlow
	expect(t, "submit valid code", alice.do("POST", "/agents/"+ag.Id+"/harness/auth/submit", gen.SubmitHarnessAuthJSONBody{
		FlowId: flow.FlowId,
		Code:   "valid-code-123",
	}, &doneFlow), 200)
	if doneFlow.Status != gen.HarnessAuthFlowStatusCompleted {
		t.Fatalf("expected completed, got %+v", doneFlow)
	}

	var refreshed gen.Agent
	expect(t, "get agent after auth", alice.do("GET", "/agents/"+ag.Id, nil, &refreshed), 200)
	if !refreshed.CredentialsInSandbox {
		t.Fatal("expected credentialsInSandbox to be true after Antigravity auth")
	}

	// Start and cancel a second flow (ensures no goroutine/process leaks).
	var flow2 gen.HarnessAuthFlow
	expect(t, "start second flow", alice.do("POST", "/agents/"+ag.Id+"/harness/auth/start", nil, &flow2), 200)
	expect(t, "cancel second flow", alice.do("POST", "/agents/"+ag.Id+"/harness/auth/cancel", gen.CancelHarnessAuthJSONBody{
		FlowId: &flow2.FlowId,
	}, nil), 204)
}
