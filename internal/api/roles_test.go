package api_test

import (
	"net/http"
	"testing"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/auth/authtest"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/store/db"
)

// roleEnv is an environment with the shipped default (Disabled) and quota (2).
func roleEnv(t *testing.T, configure func(*config.Config)) *env {
	t.Helper()
	return newEnvWith(t, func(c *config.Config) {
		c.Auth.DefaultRole = ""
		c.Auth.UserMaxAgents = nil
		if configure != nil {
			configure(c)
		}
	})
}

// githubLogin signs in u through the fake GitHub; ok is false when the
// sign-in was refused (the browser lands on /login?error=...).
func (e *env) githubLogin(u authtest.GitHubUser) (*client, bool) {
	e.t.Helper()
	e.github.SignInAs(u)
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/github")
	if err != nil {
		e.t.Fatal(err)
	}
	_ = res.Body.Close()
	return c, res.Request.URL.Path != "/login"
}

// oidcLogin signs in u through the fake OIDC provider; ok as for githubLogin.
func (e *env) oidcLogin(u authtest.User) (*client, bool) {
	e.t.Helper()
	e.idp.SignInAs(u)
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/oidc")
	if err != nil {
		e.t.Fatal(err)
	}
	_ = res.Body.Close()
	return c, res.Request.URL.Path != "/login"
}

func (c *client) meFull() gen.Me {
	c.t.Helper()
	var m gen.Me
	expect(c.t, "me", c.do("GET", "/me", nil, &m), 200)
	return m
}

func TestGitHubRoles(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		gh := &c.Auth.Providers[1]
		gh.GitHubAllowedIDs = []string{"100"}
		gh.GitHubRoles = map[config.Role][]string{config.RoleAdmin: {"9765622"}, config.RoleDisabled: {"300"}}
	})
	admin, ok := e.githubLogin(authtest.GitHubUser{ID: 9765622, Login: "gempir"})
	if !ok {
		t.Fatal("admin refused")
	}
	if m := admin.meFull(); m.Role != gen.RoleAdmin || !m.Admin || m.Agents.Max != nil {
		t.Fatalf("admin me = %+v", m)
	}
	expect(t, "overview", admin.do("GET", "/admin/overview", nil, nil), 200)
	expect(t, "nodes", admin.do("GET", "/nodes", nil, nil), 200)

	user, ok := e.githubLogin(authtest.GitHubUser{ID: 100, Login: "allowed"})
	if !ok {
		t.Fatal("allowed id refused")
	}
	if m := user.meFull(); m.Role != gen.RoleUser || m.Admin || m.Agents.Max == nil || *m.Agents.Max != 2 {
		t.Fatalf("user me = %+v", m)
	}
	expect(t, "overview as user", user.do("GET", "/admin/overview", nil, nil), 403)
	expect(t, "nodes as user", user.do("GET", "/nodes", nil, nil), 403)
	expect(t, "users as user", user.do("GET", "/admin/users", nil, nil), 403)
	expect(t, "pin a node as user", user.do("POST", "/agents", gen.AgentInput{Name: "x", Harness: ptr("fake"), NodeId: ptr("local")}, nil), 403)

	// Listed as disabled: may sign in (roles count even beside allowed ids), sees nothing.
	disabled, ok := e.githubLogin(authtest.GitHubUser{ID: 300, Login: "blocked"})
	if !ok {
		t.Fatal("a listed disabled id is refused instead of signed in")
	}
	if m := disabled.meFull(); m.Role != gen.RoleDisabled {
		t.Fatalf("disabled me = %+v", m)
	}
	// Not listed anywhere while github_allowed_ids is set: refused as before.
	if _, ok := e.githubLogin(authtest.GitHubUser{ID: 400, Login: "stranger"}); ok {
		t.Fatal("stranger signed in")
	}
}

func TestGitHubDefaultRole(t *testing.T) {
	e := roleEnv(t, nil)
	c, ok := e.githubLogin(authtest.GitHubUser{ID: 7, Login: "anyone"})
	if !ok {
		t.Fatal("refused without github_allowed_ids")
	}
	if m := c.meFull(); m.Role != gen.RoleDisabled {
		t.Fatalf("default role = %s, want disabled", m.Role)
	}
}

func TestOIDCRoleAttributePath(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.Providers[0].RoleAttributePath = "contains(resource_access.agenthold.roles[*], 'admin') && 'Admin' || contains(roles[*], 'member') && 'User'"
		c.Auth.Providers[0].DefaultRole = config.RoleUser
	})
	admin, _ := e.oidcLogin(authtest.User{Subject: "a", Name: "A", Claims: map[string]any{
		"resource_access": map[string]any{"agenthold": map[string]any{"roles": []string{"admin"}}},
	}})
	if m := admin.meFull(); m.Role != gen.RoleAdmin {
		t.Fatalf("client role admin: %s", m.Role)
	}
	// Only the userinfo response carries the roles.
	member, _ := e.oidcLogin(authtest.User{Subject: "m", Name: "M", UserInfo: map[string]any{"roles": []string{"member"}}})
	if m := member.meFull(); m.Role != gen.RoleUser {
		t.Fatalf("userinfo role: %s", m.Role)
	}
	// No match: the provider's default_role.
	other, _ := e.oidcLogin(authtest.User{Subject: "o", Name: "O"})
	if m := other.meFull(); m.Role != gen.RoleUser {
		t.Fatalf("default: %s", m.Role)
	}
	// The role is evaluated at every sign-in: a removed role is gone.
	again, _ := e.oidcLogin(authtest.User{Subject: "a", Name: "A"})
	if m := again.meFull(); m.Role != gen.RoleUser {
		t.Fatalf("after losing the client role: %s", m.Role)
	}
	// ... and the older session sees it at once, too.
	if m := admin.meFull(); m.Role != gen.RoleUser {
		t.Fatalf("old session after downgrade: %s", m.Role)
	}
}

func TestOIDCRoleAttributeStrict(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.Admins = []string{"ops@example.com"}
		c.Auth.Providers[0].RoleAttributePath = "contains(roles[*], 'agenthold') && 'User'"
		c.Auth.Providers[0].RoleAttributeStrict = true
	})
	if _, ok := e.oidcLogin(authtest.User{Subject: "x", Name: "X"}); ok {
		t.Fatal("strict let a user without a role in")
	}
	// A user the IdP takes the role away from is refused at the next
	// sign-in, and the sessions it still has lose access with it.
	old, _ := e.oidcLogin(authtest.User{Subject: "z", Name: "Z", Claims: map[string]any{"roles": []string{"agenthold"}}})
	expect(t, "before", old.do("GET", "/agents", nil, nil), 200)
	if _, ok := e.oidcLogin(authtest.User{Subject: "z", Name: "Z"}); ok {
		t.Fatal("strict let z in without the role")
	}
	expect(t, "old session after the refusal", old.do("GET", "/agents", nil, nil), 403)
	if _, err := e.store.DB.Exec("DELETE FROM users WHERE name = 'Z'"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.store.DB.QueryRow("SELECT COUNT(*) FROM users").Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused sign-in created a user (%d, %v)", n, err)
	}
	ok1, ok := e.oidcLogin(authtest.User{Subject: "y", Name: "Y", Claims: map[string]any{"roles": []string{"agenthold"}}})
	if !ok || ok1.meFull().Role != gen.RoleUser {
		t.Fatal("strict refused a user with a role")
	}
	ops, ok := e.oidcLogin(authtest.User{Subject: "ops", Name: "Ops", Email: "ops@example.com", EmailVerified: true})
	if !ok || ops.meFull().Role != gen.RoleAdmin {
		t.Fatal("auth.admins did not pass strict")
	}
	// An admin's override lets the user in although configuration would refuse.
	y := ok1.meFull().User.Id
	expect(t, "override", ops.do("PUT", "/admin/users/"+y+"/role", map[string]any{"override": "user"}, nil), 200)
	if c, ok := e.oidcLogin(authtest.User{Subject: "y", Name: "Y"}); !ok || c.meFull().Role != gen.RoleUser {
		t.Fatal("override did not win over strict")
	}
}

func TestDisabledUserIsBlocked(t *testing.T) {
	e := roleEnv(t, nil)
	c, ok := e.oidcLogin(authtest.User{Subject: "d", Name: "D", Email: "d@example.com", EmailVerified: true})
	if !ok {
		t.Fatal("a Disabled user must still be able to sign in")
	}
	m := c.meFull()
	if m.Role != gen.RoleDisabled || m.Admin {
		t.Fatalf("me = %+v", m)
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/agents"},
		{"POST", "/agents"},
		{"GET", "/me/tokens"},
		{"GET", "/secrets"},
		{"GET", "/mcp-servers"},
		{"GET", "/harnesses"},
		{"GET", "/admin/overview"},
		{"GET", "/sessions/x/stream"},
		{"GET", "/agents/x/terminal"},
	} {
		var body any
		if r.method == "POST" {
			body = gen.AgentInput{Name: "x", Harness: ptr("fake")}
		}
		expect(t, r.method+" "+r.path, c.do(r.method, r.path, body, nil), http.StatusForbidden)
	}
	expect(t, "version", c.do("GET", "/version", nil, nil), 200)
	expect(t, "providers", c.do("GET", "/auth/providers", nil, nil), 200)
	expect(t, "logout", c.do("POST", "/auth/logout", nil, nil), 204)
	expect(t, "me after logout", c.do("GET", "/me", nil, nil), 401)
}

func TestDowngradeCutsAccessAtOnce(t *testing.T) {
	e := newEnv(t) // default role user
	alice := e.login("alice", "Alice", "alice@example.com")
	var tok gen.CreatedAPIToken
	expect(t, "token", alice.do("POST", "/me/tokens", gen.CreateTokenJSONBody{Name: "ci"}, &tok), 201)
	ag := e.fakeAgent(alice, "")
	id := alice.me().Id
	if err := e.store.SetUserRoleOverride(t.Context(), db.SetUserRoleOverrideParams{RoleOverride: "disabled", UpdatedAt: 1, ID: id}); err != nil {
		t.Fatal(err)
	}
	expect(t, "agent by session", alice.do("GET", "/agents/"+ag.Id, nil, nil), 403)
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+"/api/v1/agents", nil)
	req.Header.Set("Authorization", "Bearer "+tok.Token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	expect(t, "agents by token", res.StatusCode, 403)
	// In-process callers (Slack) get nothing either.
	if _, _, err := e.auth.UserContext(t.Context(), id); err == nil {
		t.Fatal("UserContext for a Disabled user")
	}
}

func TestAgentQuota(t *testing.T) {
	e := roleEnv(t, func(c *config.Config) {
		c.Auth.DefaultRole = config.RoleUser
		c.Auth.Admins = []string{"ops@example.com"}
	})
	alice := e.login("alice", "Alice", "alice@example.com")
	first := e.fakeAgent(alice, "")
	e.fakeAgent(alice, "")
	if m := alice.meFull(); m.Agents.Used != 2 || m.Agents.Max == nil || *m.Agents.Max != 2 {
		t.Fatalf("quota = %+v", m.Agents)
	}
	expect(t, "third agent", alice.do("POST", "/agents", gen.AgentInput{Name: "three", Harness: ptr("fake")}, nil), 403)
	expect(t, "clone past the quota", alice.do("POST", "/agents/"+first.Id+"/clone", gen.CloneInput{Name: "copy"}, nil), 403)
	expect(t, "template past the quota", alice.do("POST", "/agents/"+first.Id+"/clone", gen.CloneInput{Name: "tpl", AsTemplate: ptr(true)}, nil), 403)
	expect(t, "delete", alice.do("DELETE", "/agents/"+first.Id, nil, nil), 202)
	e.sandboxes.Wait()
	expect(t, "after deleting one", alice.do("POST", "/agents", gen.AgentInput{Name: "three", Harness: ptr("fake")}, nil), 201)
	e.sandboxes.Wait()

	ops := e.login("ops", "Ops", "ops@example.com")
	for i := range 3 {
		expect(t, "admin agent", ops.do("POST", "/agents", gen.AgentInput{Name: "a" + string(rune('0'+i)), Harness: ptr("fake")}, nil), 201)
		e.sandboxes.Wait()
	}
	if m := ops.meFull(); m.Agents.Max != nil || m.Agents.Used != 3 {
		t.Fatalf("admin quota = %+v", m.Agents)
	}
}

func TestAdminUsersAndOverrides(t *testing.T) {
	e := localEnv(t, "root", "pw pw pw")
	root, status := e.localLogin("root", "pw pw pw")
	expect(t, "local login", status, 204)
	if m := root.meFull(); m.Role != gen.RoleAdmin {
		t.Fatalf("local admin role = %s", m.Role)
	}
	bob := e.login("bob", "Bob", "bob@example.com")
	bobID := bob.me().Id

	var users []gen.AdminUser
	expect(t, "users", root.do("GET", "/admin/users", nil, &users), 200)
	if len(users) != 2 || !users[0].Local || users[0].Provider != "local" || users[1].Role != gen.RoleUser || users[1].Override != nil {
		t.Fatalf("users = %+v", users)
	}
	var out gen.AdminUser
	expect(t, "make bob admin", root.do("PUT", "/admin/users/"+bobID+"/role", map[string]any{"override": "admin"}, &out), 200)
	if out.Role != gen.RoleAdmin || out.ConfigRole != gen.RoleUser || out.Override == nil || *out.Override != gen.RoleAdmin {
		t.Fatalf("after override: %+v", out)
	}
	expect(t, "bob sees the overview", bob.do("GET", "/admin/overview", nil, nil), 200)
	expect(t, "bob changes himself", bob.do("PUT", "/admin/users/"+bobID+"/role", map[string]any{"override": "disabled"}, nil), 409)
	expect(t, "bob demotes the local admin", bob.do("PUT", "/admin/users/"+root.me().Id+"/role", map[string]any{"override": "disabled"}, nil), 409)
	expect(t, "bad role", root.do("PUT", "/admin/users/"+bobID+"/role", map[string]any{"override": "viewer"}, nil), 400)
	expect(t, "unknown user", root.do("PUT", "/admin/users/nope/role", map[string]any{"override": "user"}, nil), 404)
	expect(t, "disable bob", root.do("PUT", "/admin/users/"+bobID+"/role", map[string]any{"override": "disabled"}, nil), 200)
	expect(t, "bob is out", bob.do("GET", "/agents", nil, nil), 403)
	out = gen.AdminUser{}
	expect(t, "clear", root.do("PUT", "/admin/users/"+bobID+"/role", map[string]any{"override": nil}, &out), 200)
	if out.Role != gen.RoleUser || out.Override != nil {
		t.Fatalf("after clearing: %+v", out)
	}
	expect(t, "bob is back", bob.do("GET", "/agents", nil, nil), 200)
}

func TestRolesReconciledAtStart(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	gh, _ := e.githubLogin(authtest.GitHubUser{ID: 55, Login: "octo"})
	if ops.meFull().Role != gen.RoleAdmin || gh.meFull().Role != gen.RoleUser {
		t.Fatal("roles before the restart")
	}
	// A restart with ops removed from auth.admins and octo listed as disabled.
	cfg := &config.Config{PublicURL: e.srv.URL, Auth: config.Auth{Providers: []config.Provider{
		{ID: "oidc", Type: config.ProviderOIDC, Issuer: e.idp.URL, ClientID: e.idp.ClientID},
		{ID: "github", Type: config.ProviderGitHub, ClientID: e.github.ClientID, GitHubRoles: map[config.Role][]string{config.RoleDisabled: {"55"}}},
	}, DefaultRole: config.RoleUser}}
	config.ApplyDefaults(cfg)
	svc, err := auth.NewService(e.store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.ReconcileRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if r := ops.meFull().Role; r != gen.RoleUser {
		t.Fatalf("ops after restart = %s", r)
	}
	if r := gh.meFull().Role; r != gen.RoleDisabled {
		t.Fatalf("octo after restart = %s", r)
	}
}
