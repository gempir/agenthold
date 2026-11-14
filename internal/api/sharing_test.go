package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
)

// team sets up a workspace where alice owns an agent and bob, carol and dave
// are plain members.
type team struct {
	e                       *env
	alice, bob, carol, dave *client
	ids                     map[string]string // name -> user id
	ws                      string
	agent                   gen.Agent
}

func newTeam(t *testing.T) *team {
	e := newEnv(t)
	tm := &team{e: e, ids: map[string]string{}}
	tm.alice = e.login("alice", "Alice", "a@example.com")
	tm.bob = e.login("bob", "Bob", "b@example.com")
	tm.carol = e.login("carol", "Carol", "c@example.com")
	tm.dave = e.login("dave", "Dave", "d@example.com")
	var w gen.Workspace
	tm.alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "Team"}, &w)
	tm.ws = w.Id
	for name, c := range map[string]*client{"alice": tm.alice, "bob": tm.bob, "carol": tm.carol, "dave": tm.dave} {
		var me gen.Me
		c.do("GET", "/me", nil, &me)
		tm.ids[name] = me.User.Id
		if name == "alice" {
			continue
		}
		var inv gen.CreatedInvite
		tm.alice.do("POST", "/workspaces/"+w.Id+"/invites", gen.InviteInput{Role: "member"}, &inv)
		expect(t, "accept", c.do("POST", "/invites/"+inv.Token+"/accept", nil, nil), 200)
	}
	expect(t, "agent", tm.alice.do("POST", "/workspaces/"+w.Id+"/agents", gen.AgentInput{Name: "shared", Harness: ptr("fake")}, &tm.agent), 201)
	e.sandboxes.Wait()
	return tm
}

func (tm *team) grant(who, role string) {
	tm.e.t.Helper()
	expect(tm.e.t, "grant", tm.alice.do("POST", "/agents/"+tm.agent.Id+"/grants",
		gen.GrantInput{SubjectType: "user", UserId: ptr(tm.ids[who]), Role: gen.AgentRole(role)}, nil), 200)
}

// TestRoleMatrix checks every agent endpoint for every role, server-side.
func TestRoleMatrix(t *testing.T) {
	tm := newTeam(t)
	tm.grant("bob", "viewer")
	tm.grant("carol", "operator")
	ag := tm.agent.Id
	var sess gen.Session
	tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{}, &sess)

	type call struct {
		name, method, path string
		body               any
		minRole            string // viewer, operator, admin
	}
	calls := []call{
		{"get agent", "GET", "/agents/" + ag, nil, "viewer"},
		{"list sessions", "GET", "/agents/" + ag + "/sessions", nil, "viewer"},
		{"get session", "GET", "/sessions/" + sess.Id, nil, "viewer"},
		{"session events", "GET", "/sessions/" + sess.Id + "/events", nil, "viewer"},
		{"list files", "GET", "/agents/" + ag + "/files", nil, "viewer"},
		{"egress log", "GET", "/agents/" + ag + "/egress-log", nil, "viewer"},
		{"network policy", "GET", "/agents/" + ag + "/network-policy", nil, "viewer"},
		{"access list", "GET", "/agents/" + ag + "/grants", nil, "viewer"},
		{"harness state", "GET", "/agents/" + ag + "/harness", nil, "viewer"},
		{"create session", "POST", "/agents/" + ag + "/sessions", map[string]string{}, "operator"},
		{"prompt", "POST", "/sessions/" + sess.Id + "/prompt", map[string]string{"text": "hi"}, "operator"},
		{"upload", "PUT", "/agents/" + ag + "/files/content?path=x.txt", "data", "operator"},
		{"mkdir", "POST", "/agents/" + ag + "/files/mkdir", map[string]string{"path": "d"}, "operator"},
		{"stop", "POST", "/agents/" + ag + "/stop", nil, "operator"},
		{"patch agent", "PATCH", "/agents/" + ag, gen.AgentPatch{IdleMinutes: ptr(20)}, "admin"},
		{"set policy", "PUT", "/agents/" + ag + "/network-policy", gen.NetworkPolicy{Mode: "open", Allow: []string{}}, "admin"},
		{"grant", "POST", "/agents/" + ag + "/grants", gen.GrantInput{SubjectType: "workspace", Role: "viewer"}, "admin"},
		{"audit", "GET", "/agents/" + ag + "/audit", nil, "admin"},
	}
	rank := map[string]int{"none": 0, "viewer": 1, "operator": 2, "admin": 3}
	users := []struct {
		name string
		c    *client
		role string
	}{{"dave (no grant)", tm.dave, "none"}, {"bob (viewer)", tm.bob, "viewer"}, {"carol (operator)", tm.carol, "operator"}, {"alice (owner)", tm.alice, "admin"}}

	for _, u := range users {
		for _, c := range calls {
			if c.name == "stop" {
				continue // checked separately below, it changes state
			}
			t.Run(u.name+"/"+c.name, func(t *testing.T) {
				var status int
				if s, ok := c.body.(string); ok {
					res := u.c.raw(c.method, c.path, strings.NewReader(s))
					res.Body.Close()
					status = res.StatusCode
				} else {
					status = u.c.do(c.method, c.path, c.body, nil)
				}
				tm.e.sessions.Wait()
				allowed := rank[u.role] >= rank[c.minRole]
				switch {
				case allowed && status >= 400:
					t.Fatalf("status %d, want success", status)
				case !allowed && rank[u.role] == 0 && status != http.StatusNotFound:
					t.Fatalf("status %d, want 404 for someone without access", status)
				case !allowed && rank[u.role] > 0 && status != http.StatusForbidden:
					t.Fatalf("status %d, want 403", status)
				}
			})
		}
	}
	expect(t, "viewer stops", tm.bob.do("POST", "/agents/"+ag+"/stop", nil, nil), 403)
	expect(t, "operator stops", tm.carol.do("POST", "/agents/"+ag+"/stop", nil, nil), 202)
	tm.e.sandboxes.Wait()
	expect(t, "operator deletes", tm.carol.do("DELETE", "/agents/"+ag, nil, nil), 403)
	expect(t, "viewer terminal", func() int {
		r := tm.bob.raw("GET", "/agents/"+ag+"/terminal", nil)
		r.Body.Close()
		return r.StatusCode
	}(), 403)
}

func TestViewerWatchesLiveSession(t *testing.T) {
	tm := newTeam(t)
	tm.grant("bob", "viewer")
	tm.grant("carol", "operator")
	e := tm.e
	var sess gen.Session
	expect(t, "operator creates session", tm.carol.do("POST", "/agents/"+tm.agent.Id+"/sessions", map[string]string{}, &sess), 201)

	viewer := e.stream(tm.bob, sess.Id, 0)
	op := e.stream(tm.carol, sess.Id, 0)
	expect(t, "viewer prompts", tm.bob.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hi"}, nil), 403)
	expect(t, "viewer cancels", tm.bob.do("POST", "/sessions/"+sess.Id+"/cancel", nil, nil), 403)
	expect(t, "operator prompts", tm.carol.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 5"}, nil), 202)

	got := viewer.until(isType("turn_end"))
	op.until(isType("turn_end"))
	if !strings.Contains(text(got), "chunk 5") || got[0].Data["user"].(map[string]any)["name"] != "Carol" {
		t.Fatalf("viewer saw %q (prompt by %v)", text(got), got[0].Data["user"])
	}

	// Presence: both are listed for a third viewer.
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1/sessions/" + sess.Id + "/stream?after=999999"
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: tm.alice.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if res != nil && res.Body != nil {
		res.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Type    string `json:"type"`
			Viewers []struct {
				Name, Role string
			} `json:"viewers"`
		}
		_ = json.Unmarshal(data, &m)
		if m.Type != "presence" {
			continue
		}
		names := []string{}
		for _, v := range m.Viewers {
			names = append(names, v.Name+":"+v.Role)
		}
		if strings.Join(names, ",") == "Alice:admin,Bob:viewer,Carol:operator" {
			break
		}
	}

	// The audit log records who prompted what.
	var entries []gen.AuditEntry
	expect(t, "audit", tm.alice.do("GET", "/agents/"+tm.agent.Id+"/audit", nil, &entries), 200)
	found := false
	for _, en := range entries {
		if en.Action == "session.prompt" && en.User.Name == "Carol" && en.Details["text"] == "slow 5" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no prompt entry for Carol: %+v", entries)
	}
}

func TestGrantsVisibilityAndCleanup(t *testing.T) {
	tm := newTeam(t)
	ag := tm.agent.Id
	var list []gen.Agent
	tm.bob.do("GET", "/workspaces/"+tm.ws+"/agents", nil, &list)
	if len(list) != 0 {
		t.Fatal("bob sees the agent without a grant")
	}

	// A workspace-wide grant gives every member access.
	expect(t, "ws grant", tm.alice.do("POST", "/agents/"+ag+"/grants", gen.GrantInput{SubjectType: "workspace", Role: "viewer"}, nil), 200)
	tm.bob.do("GET", "/workspaces/"+tm.ws+"/agents", nil, &list)
	if len(list) != 1 || list[0].Role != gen.AgentRoleViewer {
		t.Fatalf("bob after workspace grant: %+v", list)
	}
	// A user grant raises one member above the workspace grant.
	tm.grant("bob", "operator")
	if got := tm.bob.agent(ag); got.Role != gen.AgentRoleOperator {
		t.Fatalf("bob role %s", got.Role)
	}
	var access gen.AccessList
	tm.bob.do("GET", "/agents/"+ag+"/grants", nil, &access)
	via := map[string]string{}
	for _, en := range access.Effective {
		via[en.User.Name] = string(en.Via) + ":" + string(en.Role)
	}
	if via["Alice"] != "owner:admin" || via["Bob"] != "grant:operator" || via["Carol"] != "workspace_grant:viewer" {
		t.Fatalf("effective access %v", via)
	}

	// Non-members cannot be granted.
	outsider := tm.e.login("eve", "Eve", "e@example.com")
	var me gen.Me
	outsider.do("GET", "/me", nil, &me)
	expect(t, "grant outsider", tm.alice.do("POST", "/agents/"+ag+"/grants", gen.GrantInput{SubjectType: "user", UserId: &me.User.Id, Role: "admin"}, nil), 400)
	expect(t, "operator grants", tm.bob.do("POST", "/agents/"+ag+"/grants", gen.GrantInput{SubjectType: "user", UserId: ptr(tm.ids["carol"]), Role: "admin"}, nil), 403)

	// Leaving the workspace drops the user's grants.
	expect(t, "bob leaves", tm.bob.do("DELETE", "/workspaces/"+tm.ws+"/members/"+tm.ids["bob"], nil, nil), 204)
	var inv gen.CreatedInvite
	tm.alice.do("POST", "/workspaces/"+tm.ws+"/invites", gen.InviteInput{Role: "member"}, &inv)
	tm.bob.do("POST", "/invites/"+inv.Token+"/accept", nil, nil)
	if got := tm.bob.agent(ag); got.Role != gen.AgentRoleViewer {
		t.Fatalf("bob kept his old grant after rejoining: %s", got.Role)
	}

	// Revoke the workspace grant: carol loses access.
	tm.alice.do("GET", "/agents/"+ag+"/grants", nil, &access)
	for _, g := range access.Grants {
		if g.SubjectType == "workspace" {
			expect(t, "revoke", tm.alice.do("DELETE", "/agents/"+ag+"/grants/"+g.Id, nil, nil), 204)
		}
	}
	expect(t, "carol after revoke", tm.carol.do("GET", "/agents/"+ag, nil, nil), 404)
}
