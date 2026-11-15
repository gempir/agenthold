package api_test

import (
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
)

func (tm *team) newAgent(c *client, name string) gen.Agent {
	tm.e.t.Helper()
	var ag gen.Agent
	expect(tm.e.t, "agent "+name, c.do("POST", "/agents", gen.AgentInput{Name: name, Harness: ptr("fake")}, &ag), 201)
	tm.e.sandboxes.Wait()
	return ag
}

func TestAgentDelegatesToAgent(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	planner := tm.agent // "shared", owned by alice
	worker := tm.newAgent(alice, "worker")

	got := newSessionText(t, e, alice, planner.Id, "agents")
	if !strings.Contains(got, `"name": "worker"`) || strings.Contains(got, `"name": "shared"`) {
		t.Fatalf("list_agents: %s", got)
	}

	got = newSessionText(t, e, alice, planner.Id, "ask worker summarize the logs")
	if !strings.Contains(got, "ask -> echo: summarize the logs") {
		t.Fatalf("ask: %s", got)
	}

	// The worker's side: a linked session, prompted for alice by the planner.
	var sessions []gen.Session
	alice.do("GET", "/agents/"+worker.Id+"/sessions", nil, &sessions)
	if len(sessions) != 1 || sessions[0].Title != "From shared" {
		t.Fatalf("worker sessions %+v", sessions)
	}
	if sessions[0].Origin == nil || sessions[0].Origin.AgentId != planner.Id || sessions[0].Origin.SessionId == "" {
		t.Fatalf("origin %+v", sessions[0].Origin)
	}
	var evs []gen.SessionEvent
	alice.do("GET", "/sessions/"+sessions[0].Id+"/events", nil, &evs)
	p := evs[0].Data
	if p["user"].(map[string]any)["name"] != "agent shared (for Alice)" || p["depth"].(float64) != 1 || p["origin"].(map[string]any)["agentName"] != "shared" {
		t.Fatalf("prompt %v", p)
	}
	var audit []gen.AuditEntry
	alice.do("GET", "/agents/"+worker.Id+"/audit", nil, &audit)
	if len(audit) == 0 || audit[0].Details["fromAgent"] != "shared" {
		t.Fatalf("audit %+v", audit)
	}

	// send_message returns at once; the worker answers in the same linked session.
	got = newSessionText(t, e, alice, planner.Id, "send worker note this down")
	if !strings.Contains(got, "send_message -> Sent to worker") {
		t.Fatalf("send: %s", got)
	}
	e.sessions.Wait()
	alice.do("GET", "/sessions/"+sessions[0].Id+"/events", nil, &evs)
	if !strings.Contains(text(evs), "echo: note this down") {
		t.Fatalf("worker did not get the message: %q", text(evs))
	}

	// A sleeping target is woken.
	alice.do("POST", "/agents/"+worker.Id+"/stop", nil, nil)
	e.sandboxes.Wait()
	got = newSessionText(t, e, alice, planner.Id, "ask worker are you awake")
	if !strings.Contains(got, "echo: are you awake") {
		t.Fatalf("ask sleeping: %s", got)
	}
}

// TestAgentToAgentVisibility: an agent may address the agents its owner owns
// or that are shared with its owner, nothing else.
func TestAgentToAgentVisibility(t *testing.T) {
	tm := newTeam(t)
	e := tm.e
	bobs := tm.newAgent(tm.bob, "bobs")
	// Not shared with bob: alice's agent is invisible to bob's agent.
	if got := newSessionText(t, e, tm.bob, bobs.Id, "ask shared hello"); !strings.Contains(got, "no agent with that name") {
		t.Fatalf("unshared ask: %s", got)
	}
	if got := newSessionText(t, e, tm.bob, bobs.Id, "ask "+tm.agent.Id+" hello"); !strings.Contains(got, "no agent with that name") {
		t.Fatalf("unshared ask by id: %s", got)
	}
	if got := newSessionText(t, e, tm.bob, bobs.Id, "agents"); !strings.Contains(got, "no other agents") {
		t.Fatalf("list for bob: %s", got)
	}
	// Shared with bob: his agents can list and address it.
	tm.share("bob")
	if got := newSessionText(t, e, tm.bob, bobs.Id, "agents"); !strings.Contains(got, `"name": "shared"`) {
		t.Fatalf("list after share: %s", got)
	}
	if got := newSessionText(t, e, tm.bob, bobs.Id, "ask shared hello"); !strings.Contains(got, "echo: hello") {
		t.Fatalf("after share: %s", got)
	}
	// It does not work the other way round: alice's agent cannot reach bob's.
	if got := newSessionText(t, e, tm.alice, tm.agent.Id, "ask bobs hi"); !strings.Contains(got, "no agent with that name") {
		t.Fatalf("reverse direction: %s", got)
	}
	// Strangers' agents cannot be reached, not even by id.
	eve := e.login("eve", "Eve", "e@example.com")
	eves := tm.newAgent(eve, "eves")
	if got := newSessionText(t, e, eve, eves.Id, "ask "+tm.agent.Id+" hi"); !strings.Contains(got, "no agent with that name") {
		t.Fatalf("stranger: %s", got)
	}
	if got := newSessionText(t, e, tm.bob, bobs.Id, "ask bobs hi"); !strings.Contains(got, "cannot message itself") {
		t.Fatalf("self: %s", got)
	}
	// Unsharing takes the access away again.
	tm.unshare("bob")
	if got := newSessionText(t, e, tm.bob, bobs.Id, "ask shared hello"); !strings.Contains(got, "no agent with that name") {
		t.Fatalf("after unshare: %s", got)
	}
}

func TestLoopStoppedByDepthLimit(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	tm.newAgent(alice, "pong")
	// shared -> pong -> shared -> pong -> shared -> pong: the fifth hop exceeds the limit of 4.
	chain := "ask pong ask shared ask pong ask shared ask pong hello"
	got := newSessionText(t, e, alice, tm.agent.Id, chain)
	if !strings.Contains(got, "too many agents in a row (limit 4)") {
		t.Fatalf("loop not stopped: %s", got)
	}
	if strings.Contains(got, "echo: hello") {
		t.Fatalf("the innermost call ran: %s", got)
	}
	e.sessions.Wait()
}
