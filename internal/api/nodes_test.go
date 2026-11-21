package api_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
)

func TestOnlyAdminsManageNodes(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"Ops@Example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	dev := e.login("dev", "Dev", "dev@example.com")

	var m gen.Me
	expect(t, "me", ops.do("GET", "/me", nil, &m), 200)
	if !m.Admin {
		t.Fatal("auth.admins e-mail is not an admin")
	}
	expect(t, "me", dev.do("GET", "/me", nil, &m), 200)
	if m.Admin {
		t.Fatal("ordinary user is an admin")
	}
	expect(t, "list as user", dev.do("GET", "/nodes", nil, nil), 403)
	expect(t, "token as user", dev.do("POST", "/nodes/join-tokens", gen.CreateNodeJoinToken{}, nil), 403)
	expect(t, "delete as user", dev.do("DELETE", "/nodes/x", nil, nil), 403)

	var nodes []gen.NodeInfo
	expect(t, "list", ops.do("GET", "/nodes", nil, &nodes), 200)
	if len(nodes) != 1 {
		t.Fatalf("nodes = %+v", nodes)
	}
	expect(t, "bad ttl", ops.do("POST", "/nodes/join-tokens", gen.CreateNodeJoinToken{TtlMinutes: ptr(0)}, nil), 400)
	var tok gen.NodeJoinToken
	status := ops.do("POST", "/nodes/join-tokens", gen.CreateNodeJoinToken{Name: ptr("edge-2")}, &tok)
	if e.edge == nil {
		// A bare in-process node has no node listener.
		expect(t, "token without edge support", status, 409)
		return
	}
	expect(t, "token", status, 201)
	if !strings.HasPrefix(tok.Token, "ahnode1_") || tok.Command != "agenthold node --join "+tok.Token {
		t.Fatalf("token = %+v", tok)
	}
	if nodes[0].Local || !nodes[0].Online || nodes[0].Name != "edge" {
		t.Fatalf("edge node = %+v", nodes[0])
	}
	expect(t, "remove unknown", ops.do("DELETE", "/nodes/nope", nil, nil), 404)
}

// Admins choose the node of a new agent; everybody else gets automatic placement.
func TestAdminsPickTheNodeOfANewAgent(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	dev := e.login("dev", "Dev", "dev@example.com")
	var nodes []gen.NodeInfo
	expect(t, "list", ops.do("GET", "/nodes", nil, &nodes), 200)
	if len(nodes) == 0 {
		t.Fatal("no nodes")
	}
	pick := nodes[0]

	var ag gen.Agent
	expect(t, "pin", ops.do("POST", "/agents", gen.AgentInput{Name: "pinned", Harness: ptr("fake"), NodeId: &pick.Id}, &ag), 201)
	if ag.NodeId == nil || *ag.NodeId != pick.Id || ag.NodeName == nil || *ag.NodeName != pick.Name {
		t.Fatalf("pinned agent is on %v (%v), want %s", ag.NodeId, ag.NodeName, pick.Id)
	}
	e.sandboxes.Wait()
	if got := ops.agent(ag.Id); got.State != gen.SandboxStateRunning || got.NodeId == nil || *got.NodeId != pick.Id {
		t.Fatalf("pinned agent = %+v", got)
	}

	// An empty node is automatic placement, for everyone.
	expect(t, "empty node", dev.do("POST", "/agents", gen.AgentInput{Name: "auto", Harness: ptr("fake"), NodeId: ptr("")}, &ag), 201)
	if ag.NodeId == nil || *ag.NodeId == "" {
		t.Fatalf("automatic agent has no node: %+v", ag)
	}
	// A regular user does not choose.
	before := agentCount(t, dev)
	expect(t, "pin as user", dev.do("POST", "/agents", gen.AgentInput{Name: "nope", Harness: ptr("fake"), NodeId: &pick.Id}, nil), 403)
	if got := agentCount(t, dev); got != before {
		t.Fatalf("a refused request created an agent (%d -> %d)", before, got)
	}

	status, msg := createError(t, ops, gen.AgentInput{Name: "x", Harness: ptr("fake"), NodeId: ptr("node_nope")})
	if status != 400 || !strings.Contains(msg, `unknown node \"node_nope\"`) {
		t.Fatalf("unknown node: %d %s", status, msg)
	}
	if got := agentCount(t, ops); got != 1 {
		t.Fatalf("a refused request created an agent: ops has %d", got)
	}
}

// A node that is offline cannot take a pinned agent, and automatic placement has nothing left to choose from.
func TestPinnedAgentNeedsAnOnlineNode(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	if e.edge == nil {
		t.Skip("needs AGENTHOLD_TEST_NODE=remote")
	}
	ops := e.login("ops", "Ops", "ops@example.com")
	var nodes []gen.NodeInfo
	expect(t, "list", ops.do("GET", "/nodes", nil, &nodes), 200)
	pick := nodes[0]

	e.edge.down()
	status, msg := createError(t, ops, gen.AgentInput{Name: "x", Harness: ptr("fake"), NodeId: &pick.Id})
	if status != 409 || !strings.Contains(msg, pick.Name+" is offline") {
		t.Fatalf("offline node: %d %s", status, msg)
	}
	expect(t, "automatic without nodes", ops.do("POST", "/agents", gen.AgentInput{Name: "x", Harness: ptr("fake")}, nil), 503)
	e.edge.up()
	expect(t, "online again", ops.do("POST", "/agents", gen.AgentInput{Name: "y", Harness: ptr("fake"), NodeId: &pick.Id}, nil), 201)
}

func TestLocalAdminIsAdmin(t *testing.T) {
	e := localEnv(t, "root", "pw pw pw")
	c, status := e.localLogin("root", "pw pw pw")
	expect(t, "login", status, 204)
	var m gen.Me
	expect(t, "me", c.do("GET", "/me", nil, &m), 200)
	if !m.Admin {
		t.Fatal("the local admin manages nodes")
	}
	expect(t, "list", c.do("GET", "/nodes", nil, nil), 200)
}

// The agent of a disconnected node is unreachable, not in error; operations
// fail fast and everything works again once the node is back.
func TestOfflineNodeMakesAgentUnreachable(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	if e.edge == nil {
		t.Skip("needs AGENTHOLD_TEST_NODE=remote")
	}
	ops := e.login("ops", "Ops", "ops@example.com")
	var ag gen.Agent
	expect(t, "create", ops.do("POST", "/agents", gen.AgentInput{Name: "edge agent", Harness: ptr("fake")}, &ag), 201)
	e.sandboxes.Wait()
	if got := ops.agent(ag.Id); got.State != gen.SandboxStateRunning || got.NodeName == nil || *got.NodeName != "edge" {
		t.Fatalf("agent = %+v", got)
	}
	expect(t, "write", ops.rawStatus("PUT", "/agents/"+ag.Id+"/files/content?path=kept.txt", "kept"), 204)

	e.edge.down()
	got := ops.agent(ag.Id)
	if got.State != gen.SandboxStateUnreachable {
		t.Fatalf("state while offline = %s", got.State)
	}
	res := ops.raw("POST", "/agents/"+ag.Id+"/stop", nil)
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable || !strings.Contains(string(body), "edge is offline") {
		t.Fatalf("stop while offline: %d %s", res.StatusCode, body)
	}
	expect(t, "read while offline", ops.rawStatus("GET", "/agents/"+ag.Id+"/files/content?path=kept.txt", ""), 503)
	expect(t, "remove node with agents", ops.do("DELETE", "/nodes/"+*got.NodeId, nil, nil), 409)

	e.edge.up()
	if got := ops.agent(ag.Id); got.State != gen.SandboxStateRunning {
		t.Fatalf("state after reconnect = %s (%s)", got.State, got.StateError)
	}
	expect(t, "read after reconnect", ops.rawStatus("GET", "/agents/"+ag.Id+"/files/content?path=kept.txt", ""), 200)
}

// createError posts an agent and returns the status and the raw error body.
func createError(t *testing.T, c *client, in gen.AgentInput) (int, string) {
	t.Helper()
	return c.rawJSON("POST", "/agents", in)
}

func agentCount(t *testing.T, c *client) int {
	t.Helper()
	var list []gen.Agent
	expect(t, "list agents", c.do("GET", "/agents", nil, &list), 200)
	return len(list)
}

func (c *client) rawStatus(method, path, body string) int {
	c.t.Helper()
	res := c.raw(method, path, strings.NewReader(body))
	_ = res.Body.Close()
	return res.StatusCode
}
