package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/auth/authtest"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/store"
)

type env struct {
	t      *testing.T
	srv    *httptest.Server
	idp    *authtest.IdP
	github *authtest.GitHub
}

func newEnv(t *testing.T) *env {
	t.Helper()
	idp := authtest.NewIdP(t)
	gh := authtest.NewGitHub(t)

	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	cfg := &config.Config{PublicURL: srv.URL, Auth: config.Auth{Providers: []config.Provider{
		{ID: "oidc", Type: config.ProviderOIDC, Name: "Test IdP", Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret},
		{ID: "github", Type: config.ProviderGitHub, Name: "GitHub", ClientID: gh.ClientID, ClientSecret: gh.ClientSecret, GitHubURL: gh.URL, GitHubAPIURL: gh.URL + "/api"},
	}}}
	if err := config.Parse(nil, cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Auth.SessionTTL = 24 * 3600 * 1e9
	for i := range cfg.Auth.Providers {
		if cfg.Auth.Providers[i].Scopes == nil {
			cfg.Auth.Providers[i].Scopes = []string{"openid", "email", "profile"}
		}
	}

	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	authSvc, err := auth.NewService(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := api.New(api.Deps{Store: st, Auth: authSvc, PublicURL: srv.URL})
	handler = server.New(server.Config{
		Web: fstest.MapFS{"index.html": {Data: []byte("app")}},
		API: a.Mount,
	})
	return &env{t: t, srv: srv, idp: idp, github: gh}
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (e *env) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.srv.URL, http: &http.Client{Jar: jar}}
}

// login signs in through the fake OIDC provider and returns a client holding the session.
func (e *env) login(sub, name, email string) *client {
	e.idp.SignInAs(authtest.User{Subject: sub, Name: name, Email: email, EmailVerified: true})
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/oidc?redirect=/after")
	if err != nil {
		e.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/after" {
		e.t.Fatalf("login ended at %s", res.Request.URL)
	}
	return c
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.base+"/api/v1"+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode < 300 {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func expect(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d", what, got, want)
	}
}

func TestPublicAndUnauthenticated(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	var providers []gen.AuthProvider
	expect(t, "providers", c.do("GET", "/auth/providers", nil, &providers), 200)
	if len(providers) != 2 || providers[0].Id != "oidc" || providers[1].Type != gen.Github {
		t.Fatalf("providers = %+v", providers)
	}
	expect(t, "me", c.do("GET", "/me", nil, nil), 401)
	expect(t, "workspaces", c.do("GET", "/workspaces", nil, nil), 401)
	expect(t, "unknown provider", c.do("GET", "/auth/login/nope", nil, nil), 404)
}

func TestFirstLoginCreatesPersonalWorkspace(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice-sub", "Alice", "alice@example.com")

	var me gen.Me
	expect(t, "me", alice.do("GET", "/me", nil, &me), 200)
	if me.User.Name != "Alice" || me.User.Email != "alice@example.com" || me.PersonalWorkspaceId == "" {
		t.Fatalf("me = %+v", me)
	}
	var wss []gen.Workspace
	alice.do("GET", "/workspaces", nil, &wss)
	if len(wss) != 1 || !wss[0].Personal || wss[0].Role != gen.WorkspaceRoleOwner || wss[0].Id != me.PersonalWorkspaceId {
		t.Fatalf("workspaces = %+v", wss)
	}
	expect(t, "delete personal", alice.do("DELETE", "/workspaces/"+me.PersonalWorkspaceId, nil, nil), 409)

	// Second login: same user, still one workspace.
	again := e.login("alice-sub", "Alice A.", "alice@example.com")
	var me2 gen.Me
	again.do("GET", "/me", nil, &me2)
	if me2.User.Id != me.User.Id || me2.User.Name != "Alice A." {
		t.Fatalf("second login me = %+v", me2)
	}
	again.do("GET", "/workspaces", nil, &wss)
	if len(wss) != 1 {
		t.Fatalf("workspaces after second login = %d", len(wss))
	}
}

func TestInviteAndRoles(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "alice@example.com")
	bob := e.login("bob", "Bob", "bob@example.com")
	carol := e.login("carol", "Carol", "carol@example.com")

	var team gen.Workspace
	expect(t, "create", alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "Team"}, &team), 201)
	expect(t, "empty name", alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "  "}, nil), 400)
	ws := "/workspaces/" + team.Id

	expect(t, "outsider get", bob.do("GET", ws, nil, nil), 404)
	expect(t, "outsider invite", bob.do("POST", ws+"/invites", gen.InviteInput{Role: "member"}, nil), 404)

	var inv gen.CreatedInvite
	expect(t, "invite", alice.do("POST", ws+"/invites", gen.InviteInput{Role: "member"}, &inv), 201)
	if !strings.HasSuffix(inv.Url, "/invite/"+inv.Token) {
		t.Fatalf("invite url %q", inv.Url)
	}
	expect(t, "owner invite", alice.do("POST", ws+"/invites", map[string]string{"role": "owner"}, nil), 400)

	var preview gen.InvitePreview
	expect(t, "preview", bob.do("GET", "/invites/"+inv.Token, nil, &preview), 200)
	if preview.WorkspaceName != "Team" || preview.InvitedBy != "Alice" || preview.AlreadyMember {
		t.Fatalf("preview = %+v", preview)
	}
	var joined gen.Workspace
	expect(t, "accept", bob.do("POST", "/invites/"+inv.Token+"/accept", nil, &joined), 200)
	if joined.Id != team.Id || joined.Role != gen.WorkspaceRoleMember {
		t.Fatalf("joined = %+v", joined)
	}
	expect(t, "reuse invite", carol.do("POST", "/invites/"+inv.Token+"/accept", nil, nil), 404)

	var members []gen.Member
	expect(t, "member lists members", bob.do("GET", ws+"/members", nil, &members), 200)
	if len(members) != 2 {
		t.Fatalf("members = %+v", members)
	}
	bobID := ""
	for _, m := range members {
		if m.User.Name == "Bob" {
			bobID = m.User.Id
		}
	}

	// A member cannot manage.
	expect(t, "member invites", bob.do("POST", ws+"/invites", gen.InviteInput{Role: "member"}, nil), 403)
	expect(t, "member lists invites", bob.do("GET", ws+"/invites", nil, nil), 403)
	expect(t, "member renames", bob.do("PATCH", ws, gen.WorkspaceInput{Name: "Mine"}, nil), 403)
	expect(t, "member promotes self", bob.do("PATCH", ws+"/members/"+bobID, map[string]string{"role": "admin"}, nil), 403)

	// Promote to admin; admin can invite and rename but not delete or touch owners.
	expect(t, "promote", alice.do("PATCH", ws+"/members/"+bobID, map[string]string{"role": "admin"}, nil), 200)
	expect(t, "admin invites", bob.do("POST", ws+"/invites", gen.InviteInput{Role: "admin"}, nil), 201)
	expect(t, "admin renames", bob.do("PATCH", ws, gen.WorkspaceInput{Name: "Team 2"}, nil), 200)
	expect(t, "admin deletes", bob.do("DELETE", ws, nil, nil), 403)
	var me gen.Me
	alice.do("GET", "/me", nil, &me)
	expect(t, "admin demotes owner", bob.do("PATCH", ws+"/members/"+me.User.Id, map[string]string{"role": "member"}, nil), 403)
	expect(t, "admin makes owner", bob.do("PATCH", ws+"/members/"+bobID, map[string]string{"role": "owner"}, nil), 403)

	// Last owner protection.
	expect(t, "last owner leaves", alice.do("DELETE", ws+"/members/"+me.User.Id, nil, nil), 409)
	expect(t, "last owner demotes self", alice.do("PATCH", ws+"/members/"+me.User.Id, map[string]string{"role": "admin"}, nil), 409)

	// Invites list and revoke.
	var pending []gen.Invite
	expect(t, "list invites", alice.do("GET", ws+"/invites", nil, &pending), 200)
	if len(pending) != 1 {
		t.Fatalf("pending = %+v", pending)
	}
	expect(t, "revoke", alice.do("DELETE", ws+"/invites/"+pending[0].Id, nil, nil), 204)
	expect(t, "revoke again", alice.do("DELETE", ws+"/invites/"+pending[0].Id, nil, nil), 404)

	// Leaving works for non-owners; then the workspace is invisible.
	expect(t, "bob leaves", bob.do("DELETE", ws+"/members/"+bobID, nil, nil), 204)
	expect(t, "bob after leaving", bob.do("GET", ws, nil, nil), 404)

	expect(t, "owner deletes", alice.do("DELETE", ws, nil, nil), 204)
	expect(t, "deleted", alice.do("GET", ws, nil, nil), 404)
}

func TestInviteRestrictedToEmail(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "alice@example.com")
	bob := e.login("bob", "Bob", "bob@example.com")
	mallory := e.login("mallory", "Mallory", "mallory@example.com")

	var team gen.Workspace
	alice.do("POST", "/workspaces", gen.WorkspaceInput{Name: "Team"}, &team)
	email := "Bob@Example.com"
	var inv gen.CreatedInvite
	alice.do("POST", "/workspaces/"+team.Id+"/invites", gen.InviteInput{Role: "member", Email: &email}, &inv)

	expect(t, "wrong email", mallory.do("POST", "/invites/"+inv.Token+"/accept", nil, nil), 403)
	expect(t, "right email", bob.do("POST", "/invites/"+inv.Token+"/accept", nil, nil), 200)
	expect(t, "unknown token", bob.do("GET", "/invites/nope", nil, nil), 404)
}

func TestGitHubLogin(t *testing.T) {
	e := newEnv(t)
	e.github.SignInAs(authtest.GitHubUser{ID: 4242, Login: "octo", Name: "", PrimaryEmail: "octo@example.com", EmailVerified: true})
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/github")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/" {
		t.Fatalf("ended at %s", res.Request.URL)
	}
	var me gen.Me
	expect(t, "me", c.do("GET", "/me", nil, &me), 200)
	if me.User.Name != "octo" || me.User.Email != "octo@example.com" || !strings.Contains(me.User.AvatarUrl, "octo") {
		t.Fatalf("me = %+v", me)
	}
}

func TestLoginRejectsBadStateAndOpenRedirect(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/callback/oidc?code=x&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/login" || res.Request.URL.Query().Get("error") == "" {
		t.Fatalf("forged state ended at %s", res.Request.URL)
	}
	expect(t, "no session", c.do("GET", "/me", nil, nil), 401)

	e.idp.SignInAs(authtest.User{Subject: "s", Name: "S"})
	for _, target := range []string{"//evil.example", "https://evil.example/", "/\\evil.example"} {
		res, err = c.http.Get(c.base + "/api/v1/auth/login/oidc?redirect=" + target)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.Request.URL.Host != strings.TrimPrefix(e.srv.URL, "http://") || res.Request.URL.Path != "/" {
			t.Fatalf("redirect %q ended at %s", target, res.Request.URL)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	expect(t, "me", alice.do("GET", "/me", nil, nil), 200)
	expect(t, "logout", alice.do("POST", "/auth/logout", nil, nil), 204)
	expect(t, "me after logout", alice.do("GET", "/me", nil, nil), 401)
}
