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
	"github.com/gempir/agenthold/internal/auth/authtest"
)

// team: alice owns an agent; bob, carol and dave have signed in.
type team struct {
	e                       *env
	alice, bob, carol, dave *client
	ids, emails             map[string]string // name -> user id, email
	agent                   gen.Agent
}

func newTeam(t *testing.T) *team {
	e := newEnv(t)
	tm := &team{e: e, ids: map[string]string{}, emails: map[string]string{}}
	tm.alice = e.login("alice", "Alice", "a@example.com")
	tm.bob = e.login("bob", "Bob", "b@example.com")
	tm.carol = e.login("carol", "Carol", "c@example.com")
	tm.dave = e.login("dave", "Dave", "d@example.com")
	for name, c := range map[string]*client{"alice": tm.alice, "bob": tm.bob, "carol": tm.carol, "dave": tm.dave} {
		u := c.me()
		tm.ids[name], tm.emails[name] = u.Id, u.Email
	}
	expect(t, "agent", tm.alice.do("POST", "/agents", gen.AgentInput{Name: "shared", Harness: ptr("fake")}, &tm.agent), 201)
	e.sandboxes.Wait()
	return tm
}

// share makes alice share her agent with who.
func (tm *team) share(who string) {
	tm.e.t.Helper()
	expect(tm.e.t, "share with "+who, tm.alice.do("POST", "/agents/"+tm.agent.Id+"/shares", gen.ShareInput{Email: tm.emails[who]}, nil), 200)
}

// unshare makes alice stop sharing her agent with who.
func (tm *team) unshare(who string) {
	tm.e.t.Helper()
	expect(tm.e.t, "unshare "+who, tm.alice.do("DELETE", "/agents/"+tm.agent.Id+"/shares/"+tm.ids[who], nil, nil), 204)
}

// TestAccessMatrix checks the agent endpoints for the owner, a shared user
// and a stranger, server-side.
func TestAccessMatrix(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	ag := tm.agent.Id
	var sess gen.Session
	tm.alice.do("POST", "/agents/"+ag+"/sessions", map[string]string{}, &sess)

	type call struct {
		name, method, path string
		body               any
		owner              bool // owner only; otherwise shared users may too
	}
	calls := []call{
		{"get agent", "GET", "/agents/" + ag, nil, false},
		{"list sessions", "GET", "/agents/" + ag + "/sessions", nil, false},
		{"get session", "GET", "/sessions/" + sess.Id, nil, false},
		{"session events", "GET", "/sessions/" + sess.Id + "/events", nil, false},
		{"list files", "GET", "/agents/" + ag + "/files", nil, false},
		{"access list", "GET", "/agents/" + ag + "/shares", nil, false},
		{"harness state", "GET", "/agents/" + ag + "/harness", nil, false},
		{"agent mcp servers", "GET", "/agents/" + ag + "/mcp-servers", nil, false},
		{"agent skills", "GET", "/agents/" + ag + "/skills", nil, false},
		{"agent secrets", "GET", "/agents/" + ag + "/secrets", nil, false},
		{"schedules", "GET", "/agents/" + ag + "/schedules", nil, false},
		{"create session", "POST", "/agents/" + ag + "/sessions", map[string]string{}, false},
		{"prompt", "POST", "/sessions/" + sess.Id + "/prompt", map[string]string{"text": "hi"}, false},
		{"upload", "PUT", "/agents/" + ag + "/files/content?path=x.txt", "data", false},
		{"mkdir", "POST", "/agents/" + ag + "/files/mkdir", map[string]string{"path": "d"}, false},
		{"patch agent", "PATCH", "/agents/" + ag, gen.AgentPatch{IdleMinutes: ptr(20)}, true},
		{"share", "POST", "/agents/" + ag + "/shares", gen.ShareInput{Email: tm.emails["carol"]}, true},
		{"audit", "GET", "/agents/" + ag + "/audit", nil, true},
		{"agent mcp server", "POST", "/agents/" + ag + "/mcp-servers", gen.MCPServerInput{Name: "m", Transport: "stdio", Command: ptr("true")}, true},
		{"use my logins", "PUT", "/agents/" + ag + "/login", nil, true},
		{"clone", "POST", "/agents/" + ag + "/clone", gen.CloneInput{Name: "copy"}, true},
	}
	users := []struct {
		name   string
		c      *client
		access string // none, shared, owner
	}{{"dave (stranger)", tm.dave, "none"}, {"bob (shared)", tm.bob, "shared"}, {"alice (owner)", tm.alice, "owner"}}

	for _, u := range users {
		for _, c := range calls {
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
				tm.e.sandboxes.Wait()
				allowed := u.access == "owner" || (u.access == "shared" && !c.owner)
				switch {
				case allowed && status >= 400:
					t.Fatalf("status %d, want success", status)
				case !allowed && u.access == "none" && status != http.StatusNotFound:
					t.Fatalf("status %d, want 404 for someone without access", status)
				case !allowed && u.access == "shared" && status != http.StatusForbidden:
					t.Fatalf("status %d, want 403", status)
				}
			})
		}
	}

	// State-changing calls, checked once each.
	expect(t, "stranger stops", tm.dave.do("POST", "/agents/"+ag+"/stop", nil, nil), 404)
	expect(t, "shared user stops", tm.bob.do("POST", "/agents/"+ag+"/stop", nil, nil), 202)
	tm.e.sandboxes.Wait()
	expect(t, "shared user starts", tm.bob.do("POST", "/agents/"+ag+"/start", nil, nil), 202)
	tm.e.sandboxes.Wait()
	expect(t, "shared user resets", tm.bob.do("POST", "/agents/"+ag+"/rebuild", nil, nil), 403)
	expect(t, "shared user deletes", tm.bob.do("DELETE", "/agents/"+ag, nil, nil), 403)
	expect(t, "stranger deletes", tm.dave.do("DELETE", "/agents/"+ag, nil, nil), 404)
	for _, tc := range []struct {
		name, path string
		c          *client
		want       int
	}{
		{"stranger terminal", "", tm.dave, http.StatusNotFound},
		{"shared user harness login", "?auth=x", tm.bob, http.StatusForbidden},
	} {
		r := tc.c.raw("GET", "/agents/"+ag+"/terminal"+tc.path, nil)
		r.Body.Close()
		expect(t, tc.name, r.StatusCode, tc.want)
	}
	// The shared user's terminal is not refused (a plain GET is not a WebSocket upgrade).
	r := tm.bob.raw("GET", "/agents/"+ag+"/terminal", nil)
	r.Body.Close()
	if r.StatusCode == http.StatusForbidden || r.StatusCode == http.StatusNotFound {
		t.Fatalf("shared user terminal: %d", r.StatusCode)
	}
	expect(t, "owner resets", tm.alice.do("POST", "/agents/"+ag+"/rebuild", nil, nil), 202)
	tm.e.sandboxes.Wait()
	expect(t, "owner deletes", tm.alice.do("DELETE", "/agents/"+ag, nil, nil), 202)
	tm.e.sandboxes.Wait()
	expect(t, "deleted", tm.bob.do("GET", "/agents/"+ag, nil, nil), 404)
}

func TestShareByEmail(t *testing.T) {
	tm := newTeam(t)
	e, ag := tm.e, tm.agent.Id
	path := "/agents/" + ag + "/shares"

	var list []gen.Agent
	tm.bob.do("GET", "/agents", nil, &list)
	if len(list) != 0 {
		t.Fatal("bob sees the agent before it is shared")
	}

	// Found by verified email, case-insensitively.
	var sh gen.Share
	expect(t, "share", tm.alice.do("POST", path, gen.ShareInput{Email: " B@Example.COM "}, &sh), 200)
	if sh.User.Id != tm.ids["bob"] {
		t.Fatalf("shared with %+v", sh.User)
	}
	expect(t, "share again", tm.alice.do("POST", path, gen.ShareInput{Email: "b@example.com"}, nil), 200)
	tm.bob.do("GET", "/agents", nil, &list)
	if len(list) != 1 || list[0].Id != ag || list[0].Role != gen.Shared || list[0].Owner.Id != tm.ids["alice"] {
		t.Fatalf("bob's agents %+v", list)
	}
	if got := tm.alice.agent(ag); got.Role != gen.Owner {
		t.Fatalf("alice role %s", got.Role)
	}

	// Who can be shared with.
	expect(t, "unknown email", tm.alice.do("POST", path, gen.ShareInput{Email: "nobody@example.com"}, nil), 404)
	expect(t, "not an email", tm.alice.do("POST", path, gen.ShareInput{Email: "bob"}, nil), 400)
	expect(t, "own email", tm.alice.do("POST", path, gen.ShareInput{Email: "a@example.com"}, nil), 400)
	e.idp.SignInAs(authtest.User{Subject: "mallory", Name: "Mallory", Email: "m@example.com", EmailVerified: false})
	mallory := e.anon()
	if res, err := mallory.http.Get(mallory.base + "/api/v1/auth/login/oidc"); err == nil {
		res.Body.Close()
	}
	expect(t, "unverified email", tm.alice.do("POST", path, gen.ShareInput{Email: "m@example.com"}, nil), 404)
	// Two accounts with the same verified email (different providers): ambiguous.
	e.github.SignInAs(authtest.GitHubUser{ID: 77, Login: "dave2", PrimaryEmail: "d@example.com", EmailVerified: true})
	gh := e.anon()
	if res, err := gh.http.Get(gh.base + "/api/v1/auth/login/github"); err == nil {
		res.Body.Close()
	}
	expect(t, "ambiguous email", tm.alice.do("POST", path, gen.ShareInput{Email: "d@example.com"}, nil), 409)

	// Only the owner shares.
	expect(t, "shared user shares", tm.bob.do("POST", path, gen.ShareInput{Email: "c@example.com"}, nil), 403)
	expect(t, "stranger shares", tm.carol.do("POST", path, gen.ShareInput{Email: "c@example.com"}, nil), 404)

	// Shared users see the access list.
	tm.share("carol")
	var access gen.AccessList
	expect(t, "access list", tm.bob.do("GET", path, nil, &access), 200)
	names := []string{}
	for _, s := range access.Shares {
		names = append(names, s.User.Name)
	}
	if access.Owner.Name != "Alice" || strings.Join(names, ",") != "Bob,Carol" {
		t.Fatalf("access %s / %v", access.Owner.Name, names)
	}

	// A shared user cannot remove others, but can leave.
	expect(t, "bob removes carol", tm.bob.do("DELETE", path+"/"+tm.ids["carol"], nil, nil), 403)
	expect(t, "bob leaves", tm.bob.do("DELETE", path+"/"+tm.ids["bob"], nil, nil), 204)
	expect(t, "bob after leaving", tm.bob.do("GET", "/agents/"+ag, nil, nil), 404)
	tm.unshare("carol")
	expect(t, "carol after unshare", tm.carol.do("GET", "/agents/"+ag, nil, nil), 404)
	expect(t, "unshare again", tm.alice.do("DELETE", path+"/"+tm.ids["carol"], nil, nil), 404)

	var audit []gen.AuditEntry
	tm.alice.do("GET", "/agents/"+ag+"/audit", nil, &audit)
	if !hasAction(audit, "share.add") || !hasAction(audit, "share.remove") {
		t.Fatalf("audit %+v", audit)
	}
}

func TestSharedUserWatchesLiveSession(t *testing.T) {
	tm := newTeam(t)
	tm.share("bob")
	tm.share("carol")
	e := tm.e
	var sess gen.Session
	expect(t, "shared user creates session", tm.carol.do("POST", "/agents/"+tm.agent.Id+"/sessions", map[string]string{}, &sess), 201)

	watcher := e.stream(tm.bob, sess.Id, 0)
	op := e.stream(tm.carol, sess.Id, 0)
	expect(t, "stranger prompts", tm.dave.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "hi"}, nil), 404)
	expect(t, "shared user prompts", tm.carol.do("POST", "/sessions/"+sess.Id+"/prompt", map[string]string{"text": "slow 5"}, nil), 202)

	got := watcher.until(isType("turn_end"))
	op.until(isType("turn_end"))
	if !strings.Contains(text(got), "chunk 5") || got[0].Data["user"].(map[string]any)["name"] != "Carol" {
		t.Fatalf("watcher saw %q (prompt by %v)", text(got), got[0].Data["user"])
	}

	// Presence: everyone watching is listed with their role.
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
		if strings.Join(names, ",") == "Alice:owner,Bob:shared,Carol:shared" {
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

	// Unsharing ends a live view.
	tm.unshare("bob")
	rctx, rcancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer rcancel()
	for {
		if _, _, err := watcher.ws.Read(rctx); err != nil {
			if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
				t.Fatalf("stream ended with %v, want policy violation", err)
			}
			break
		}
	}
}
