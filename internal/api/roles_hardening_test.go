package api_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/auth/authtest"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

func (e *env) disable(userID string) {
	e.t.Helper()
	if err := e.store.SetUserRoleOverride(e.t.Context(), db.SetUserRoleOverrideParams{RoleOverride: "disabled", UpdatedAt: 1, ID: userID}); err != nil {
		e.t.Fatal(err)
	}
}

// closedForPolicy reads ws until it fails and checks that the server closed
// it for a policy violation, within the recheck interval plus slack.
func closedForPolicy(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer cancel()
	for {
		_, _, err := ws.Read(ctx)
		if err == nil {
			continue
		}
		if ctx.Err() != nil {
			t.Fatal("the connection outlived the downgrade")
		}
		if got := websocket.CloseStatus(err); got != websocket.StatusPolicyViolation {
			t.Fatalf("closed with %v (%v), want a policy violation", got, err)
		}
		return
	}
}

func (e *env) dialWS(c *client, path string) *websocket.Conn {
	e.t.Helper()
	ctx, cancel := context.WithTimeout(e.t.Context(), 10*time.Second)
	defer cancel()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/api/v1" + path
	ws, res, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: http.Header{"Origin": {e.srv.URL}}})
	if res != nil && res.Body != nil {
		_ = res.Body.Close()
	}
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = ws.CloseNow() })
	return ws
}

func TestDisabledClosesTerminal(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	ag := e.agentFor(alice, "term")
	ws := e.dialWS(alice, "/agents/"+ag.Id+"/terminal?cols=80&rows=24")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for { // the shell is up once it prints
		typ, _, err := ws.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if typ == websocket.MessageBinary {
			break
		}
	}
	e.disable(alice.me().Id)
	closedForPolicy(t, ws)
}

func TestDisabledClosesSessionStream(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	id := alice.me().Id
	ag := e.fakeAgent(alice, "")
	var sess gen.Session
	expect(t, "session", alice.do("POST", "/agents/"+ag.Id+"/sessions", map[string]string{}, &sess), 201)
	st := e.stream(alice, sess.Id, 0)
	e.disable(id)
	closedForPolicy(t, st.ws)
}

func TestDisabledClosesServiceLogStream(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	ag := tm.agent.Id
	expect(t, "put", alice.do("PUT", "/agents/"+ag+"/services/web", gen.ServiceInput{Command: "echo up; exec sleep 300"}, nil), 200)
	waitUntil(t, "running", 5*time.Second, func() bool { return e.serviceState(alice, ag, "web") == "running" })
	ws := e.dialWS(alice, "/agents/"+ag+"/services/web/logs/stream?lines=10")
	e.disable(tm.ids["alice"])
	closedForPolicy(t, ws)
}

func TestDisabledClosesPreviewWebSocket(t *testing.T) {
	tm := newTeam(t)
	e, alice := tm.e, tm.alice
	app := startUpstream(t)
	ex := e.expose(alice, tm.agent.Id, app.port, "")
	ab := e.browser(alice, true)
	if r := get(t, ab, ex.Url, nil); r.status != 200 {
		t.Fatalf("owner: %d", r.status)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, strings.Replace(ex.Url, "http", "ws", 1)+"ws", &websocket.DialOptions{HTTPClient: ab})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.CloseNow() }()
	if err := ws.Write(ctx, websocket.MessageText, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if _, b, err := ws.Read(ctx); err != nil || string(b) != "echo one" {
		t.Fatalf("echo %q %v", b, err)
	}
	e.disable(tm.ids["alice"])
	rctx, rcancel := context.WithTimeout(t.Context(), 6*time.Second)
	defer rcancel()
	for {
		if _, _, err := ws.Read(rctx); err != nil {
			if rctx.Err() != nil {
				t.Fatal("the proxied WebSocket outlived the downgrade")
			}
			return // the proxy drops the connection
		}
	}
}

func TestAdminEmailDoesNotOpenGitHubAllowList(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.Admins = []string{"boss@example.com"}
		c.Auth.Providers[1].GitHubAllowedIDs = []string{"1"}
	})
	if _, ok := e.githubLogin(authtest.GitHubUser{ID: 2, Login: "boss", PrimaryEmail: "boss@example.com", EmailVerified: true}); ok {
		t.Fatal("an auth.admins e-mail got past github_allowed_ids")
	}
	c, ok := e.githubLogin(authtest.GitHubUser{ID: 1, Login: "boss1", PrimaryEmail: "boss@example.com", EmailVerified: true})
	if !ok || c.meFull().Role != gen.RoleAdmin {
		t.Fatal("an allowed id with an admin e-mail is not Admin")
	}
}

func TestSessionMaxAge(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.DefaultRole = config.RoleUser
		c.Auth.SessionMaxAge = 48 * time.Hour
	})
	now := time.Now()
	e.auth.SetClock(func() time.Time { return now })
	alice := e.login("alice", "Alice", "a@example.com")
	// Used every day, the session still ends at the cap.
	for range 3 {
		expect(t, "me", alice.do("GET", "/me", nil, nil), 200)
		now = now.Add(20 * time.Hour)
	}
	expect(t, "me after the cap", alice.do("GET", "/me", nil, nil), 401)

	// A session from before the cap, with a later expiry, ends at the cap too.
	bob := e.login("bob", "Bob", "b@example.com")
	if _, err := e.store.DB.Exec("UPDATE sessions SET created_at = created_at - 3*86400, expires_at = expires_at + 30*86400"); err != nil {
		t.Fatal(err)
	}
	expect(t, "old long session", bob.do("GET", "/me", nil, nil), 401)
}

func TestRoleMappingChangeSignsOut(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.Providers[0].RoleAttributePath = "contains(roles[*], 'x') && 'Admin'"
		c.Auth.Providers[0].DefaultRole = config.RoleUser
	})
	alice, _ := e.oidcLogin(authtest.User{Subject: "a", Name: "A", Claims: map[string]any{"roles": []string{"x"}}})
	if alice.meFull().Role != gen.RoleAdmin {
		t.Fatal("not admin before")
	}
	restart := func(path string) {
		cfg := &config.Config{PublicURL: e.srv.URL, Auth: config.Auth{Providers: []config.Provider{
			{ID: "oidc", Type: config.ProviderOIDC, Issuer: e.idp.URL, ClientID: e.idp.ClientID, RoleAttributePath: path, DefaultRole: config.RoleUser},
			{ID: "github", Type: config.ProviderGitHub, ClientID: e.github.ClientID},
		}}}
		config.ApplyDefaults(cfg)
		svc, err := auth.NewService(e.store, cfg)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.ReconcileRoles(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// The same mapping: the stored result stands, the session too.
	restart("contains(roles[*], 'x') && 'Admin'")
	if alice.meFull().Role != gen.RoleAdmin {
		t.Fatal("an unchanged mapping changed the role")
	}
	// Another mapping: the old result is void and the user signs in again.
	restart("contains(roles[*], 'y') && 'Admin'")
	expect(t, "me after the mapping changed", alice.do("GET", "/me", nil, nil), 401)
	// Until then the old result no longer counts: the user has the default.
	var role string
	if err := e.store.DB.QueryRow("SELECT role FROM users").Scan(&role); err != nil || role != "user" {
		t.Fatalf("stored role after the mapping changed = %q, %v", role, err)
	}
}
