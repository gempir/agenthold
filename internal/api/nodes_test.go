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

func (c *client) rawStatus(method, path, body string) int {
	c.t.Helper()
	res := c.raw(method, path, strings.NewReader(body))
	_ = res.Body.Close()
	return res.StatusCode
}
