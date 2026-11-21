package auth

import (
	"testing"

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
			if got := tc.expr.eval(tc.claims); got != tc.want {
				t.Fatalf("eval = %q, want %q", got, tc.want)
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
		{"admin e-mail wins over mapping", oidc, config.RoleDisabled, "Boss@example.com", true, config.RoleAdmin, true},
		{"unverified admin e-mail does not count", oidc, "", "boss@example.com", false, config.RoleDisabled, true},
		{"mapping", oidc, config.RoleUser, "", false, config.RoleUser, true},
		{"no match falls back to auth.default_role", oidc, "", "", false, config.RoleDisabled, true},
		{"provider default_role", oidcDefault, "", "", false, config.RoleUser, true},
		{"strict refuses", oidcStrict, "", "", false, config.RoleDisabled, false},
		{"strict with a match", oidcStrict, config.RoleUser, "", false, config.RoleUser, true},
		{"admin e-mail passes strict", oidcStrict, "", "boss@example.com", true, config.RoleAdmin, true},
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
