package auth

import (
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/config"
)

func mustExpr(t *testing.T, src string) *roleExpr {
	t.Helper()
	e, err := compileRoleExpr(src)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestRoleAttributePath(t *testing.T) {
	// Grafana's documented shape, with our role names.
	grafana := mustExpr(t, "contains(roles[*], 'admin') && 'Admin' || contains(roles[*], 'editor') && 'User' || 'Disabled'")
	// Keycloak client roles: resource_access.<client>.roles.
	client := mustExpr(t, "contains(resource_access.agenthold.roles[*], 'agenthold-admin') && 'admin' || contains(resource_access.agenthold.roles[*], 'agenthold-user') && 'user'")
	// Keycloak realm roles and groups.
	realm := mustExpr(t, "contains(realm_access.roles[*], 'ops') && 'Admin' || contains(groups[*], '/agenthold') && 'User'")
	for _, tc := range []struct {
		name   string
		expr   *roleExpr
		claims map[string]any
		want   config.Role
	}{
		{"admin", grafana, map[string]any{"roles": []any{"viewer", "admin"}}, config.RoleAdmin},
		{"editor maps to user", grafana, map[string]any{"roles": []any{"editor"}}, config.RoleUser},
		{"fallback", grafana, map[string]any{"roles": []any{"viewer"}}, config.RoleDisabled},
		{"no roles claim", grafana, map[string]any{}, config.RoleDisabled},
		{"client admin", client, map[string]any{"resource_access": map[string]any{"agenthold": map[string]any{"roles": []any{"agenthold-admin"}}}}, config.RoleAdmin},
		{"client user", client, map[string]any{"resource_access": map[string]any{"agenthold": map[string]any{"roles": []any{"agenthold-user"}}}}, config.RoleUser},
		{"other client's roles do not count", client, map[string]any{"resource_access": map[string]any{"grafana": map[string]any{"roles": []any{"agenthold-admin"}}}}, ""},
		{"no match yields nothing", client, map[string]any{}, ""},
		{"realm roles", realm, map[string]any{"realm_access": map[string]any{"roles": []any{"ops"}}}, config.RoleAdmin},
		{"groups", realm, map[string]any{"groups": []any{"/agenthold"}}, config.RoleUser},
		{"unknown role name is nothing", mustExpr(t, "'Editor'"), map[string]any{}, ""},
		{"non-string is nothing", mustExpr(t, "roles"), map[string]any{"roles": []any{"admin"}}, ""},
		{"from a decoded token", grafana, claimsMap([]byte(`{"sub":"x","roles":["admin"]}`)), config.RoleAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.expr.eval(tc.claims)
			if err != nil || got != tc.want {
				t.Fatalf("eval = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	if _, err := compileRoleExpr("contains(roles[*], 'admin' &&"); err == nil {
		t.Fatal("an invalid expression compiled")
	}
}

func TestGitHubRole(t *testing.T) {
	p := config.Provider{Type: config.ProviderGitHub, GitHubAllowedIDs: []string{"1", "2"}, GitHubRoles: map[config.Role][]string{
		config.RoleAdmin: {"9765622"}, config.RoleDisabled: {"2"},
	}}
	for id, want := range map[string]config.Role{"9765622": config.RoleAdmin, "1": config.RoleUser, "2": config.RoleDisabled, "3": ""} {
		if got := githubRole(p, id); got != want {
			t.Errorf("githubRole(%s) = %q, want %q", id, got, want)
		}
	}
}

func TestConfigRolePrecedence(t *testing.T) {
	s := &Service{admins: config.Auth{Admins: []string{"boss@example.com"}, DefaultRole: config.RoleDisabled}}
	oidc := config.Provider{Type: config.ProviderOIDC, RoleAttributePath: "x"}
	oidcStrict := config.Provider{Type: config.ProviderOIDC, RoleAttributePath: "x", RoleAttributeStrict: true}
	oidcDefault := config.Provider{Type: config.ProviderOIDC, DefaultRole: config.RoleUser}
	gh := config.Provider{Type: config.ProviderGitHub}
	ghAllow := config.Provider{Type: config.ProviderGitHub, GitHubAllowedIDs: []string{"1"}}
	for _, tc := range []struct {
		name     string
		p        config.Provider
		mapped   config.Role
		email    string
		verified bool
		want     config.Role
		allowed  bool
	}{
		{"admin e-mail upgrades a mapped user", oidc, config.RoleUser, "Boss@example.com", true, config.RoleAdmin, true},
		{"admin e-mail upgrades the default role", oidc, "", "boss@example.com", true, config.RoleAdmin, true},
		{"admin e-mail does not undo an explicit Disabled", oidc, config.RoleDisabled, "boss@example.com", true, config.RoleDisabled, true},
		{"unverified admin e-mail does not count", oidc, "", "boss@example.com", false, config.RoleDisabled, true},
		{"mapping", oidc, config.RoleUser, "", false, config.RoleUser, true},
		{"no match falls back to auth.default_role", oidc, "", "", false, config.RoleDisabled, true},
		{"provider default_role", oidcDefault, "", "", false, config.RoleUser, true},
		{"strict refuses", oidcStrict, "", "", false, config.RoleDisabled, false},
		{"strict with a match", oidcStrict, config.RoleUser, "", false, config.RoleUser, true},
		{"admin e-mail does not open strict", oidcStrict, "", "boss@example.com", true, config.RoleDisabled, false},
		{"admin e-mail after strict let the user in", oidcStrict, config.RoleUser, "boss@example.com", true, config.RoleAdmin, true},
		{"admin e-mail does not open github_allowed_ids", ghAllow, "", "boss@example.com", true, config.RoleDisabled, false},
		{"github without allowed ids", gh, "", "", false, config.RoleDisabled, true},
		{"github allowed ids are strict", ghAllow, "", "", false, config.RoleDisabled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, allowed := s.configRole(tc.p, tc.mapped, tc.email, tc.verified)
			if got != tc.want || allowed != tc.allowed {
				t.Fatalf("configRole = %q, %v; want %q, %v", got, allowed, tc.want, tc.allowed)
			}
		})
	}
}

func TestRoleExprFailsClosed(t *testing.T) {
	// contains() over objects must not panic (== on maps does).
	e := mustExpr(t, "contains(groups, {name: 'banned'}) && 'Disabled' || 'User'")
	got, err := e.eval(map[string]any{"groups": []any{map[string]any{"name": "banned"}, []any{"x"}}})
	if err != nil || got != config.RoleDisabled {
		t.Fatalf("object contains = %q, %v", got, err)
	}
	// An expression that cannot be evaluated is an error, never the fallback.
	bad := mustExpr(t, "abs(roles) && 'Disabled' || 'User'")
	if got, err := bad.eval(map[string]any{"roles": []any{"x"}}); err == nil || got != "" {
		t.Fatalf("type error = %q, %v; want an error", got, err)
	}
}

func TestMergedClaims(t *testing.T) {
	// A deny-list in userinfo applies although the ID token has other claims.
	deny := mustExpr(t, "contains(groups[*], 'banned') && 'Disabled' || 'User'")
	merged := mergeClaims(map[string]any{"sub": "x", "roles": []any{"a"}}, map[string]any{"sub": "x", "groups": []any{"banned"}})
	if got, _ := deny.eval(merged); got != config.RoleDisabled {
		t.Fatalf("deny from userinfo = %q", got)
	}
	// The ID token wins a conflict.
	m := mergeClaims(map[string]any{"groups": []any{"ok"}}, map[string]any{"groups": []any{"banned"}})
	if got, _ := deny.eval(m); got != config.RoleUser {
		t.Fatalf("id token should win: %q", got)
	}
}

func TestRoleConfigHash(t *testing.T) {
	a := config.Provider{Type: config.ProviderOIDC, RoleAttributePath: "'Admin'"}
	b := config.Provider{Type: config.ProviderOIDC, RoleAttributePath: "'User'"}
	if roleConfigHash(a) == "" || roleConfigHash(a) == roleConfigHash(b) {
		t.Fatal("hash does not tell expressions apart")
	}
	if roleConfigHash(config.Provider{Type: config.ProviderOIDC}) != "" || roleConfigHash(config.Provider{Type: config.ProviderGitHub}) != "" {
		t.Fatal("no mapping must hash to the empty string")
	}
}

func TestSessionExpiryIsCapped(t *testing.T) {
	s := &Service{sessionTTL: 30 * 24 * time.Hour, sessionMax: 7 * 24 * time.Hour}
	created := time.Unix(1_000_000, 0)
	if got := s.sessionExpiry(created.Unix(), created); got != created.Add(7*24*time.Hour).Unix() {
		t.Fatalf("new session expires %d", got)
	}
	later := created.Add(6 * 24 * time.Hour)
	if got := s.sessionExpiry(created.Unix(), later); got != created.Add(7*24*time.Hour).Unix() {
		t.Fatalf("a used session slid past the cap: %d", got)
	}
	short := &Service{sessionTTL: time.Hour, sessionMax: 7 * 24 * time.Hour}
	if got := short.sessionExpiry(created.Unix(), later); got != later.Add(time.Hour).Unix() {
		t.Fatalf("ttl below the cap: %d", got)
	}
}
