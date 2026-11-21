package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadYAML loads a config file and validates it, as the server does.
func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHOLD_CONFIG", path)
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	return cfg, cfg.Validate()
}

func TestRoleDefaults(t *testing.T) {
	cfg, err := loadYAML(t, "public_url: https://a.example.com\n")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.DefaultRole != RoleDisabled || cfg.Auth.UserMaxAgents == nil || *cfg.Auth.UserMaxAgents != 2 {
		t.Fatalf("defaults: role %q, max %v", cfg.Auth.DefaultRole, cfg.Auth.UserMaxAgents)
	}
}

func TestRoleConfig(t *testing.T) {
	cfg, err := loadYAML(t, `
public_url: https://a.example.com
auth:
  default_role: User
  user_max_agents: 5
  providers:
    - id: kc
      type: oidc
      issuer: https://id.example.com/realms/x
      client_id: agenthold
      role_attribute_path: "contains(resource_access.agenthold.roles[*], 'admin') && 'Admin' || 'User'"
      role_attribute_strict: true
      default_role: disabled
    - id: github
      type: github
      client_id: abc
      github_allowed_ids: [1234]
      github_roles:
        Admin: [9765622]
        user: ["42"]
`)
	if err != nil {
		t.Fatal(err)
	}
	kc, gh := cfg.Auth.Providers[0], cfg.Auth.Providers[1]
	if cfg.Auth.DefaultRole != RoleUser || *cfg.Auth.UserMaxAgents != 5 || kc.DefaultRole != RoleDisabled || !kc.RoleAttributeStrict {
		t.Fatalf("auth = %+v, kc = %+v", cfg.Auth, kc)
	}
	if got := gh.GitHubRoles[RoleAdmin]; len(got) != 1 || got[0] != "9765622" {
		t.Fatalf("github_roles = %+v", gh.GitHubRoles)
	}
	if gh.GitHubAllowedIDs[0] != "1234" {
		t.Fatalf("allowed = %+v", gh.GitHubAllowedIDs)
	}
}

func TestRoleConfigErrors(t *testing.T) {
	for name, tc := range map[string]struct{ yaml, want string }{
		"invalid expression": {`
auth:
  providers:
    - {id: kc, type: oidc, issuer: https://id.example.com, client_id: x, role_attribute_path: "contains(roles[*], 'admin' && "}
`, "role_attribute_path"},
		"strict without path": {`
auth:
  providers:
    - {id: kc, type: oidc, issuer: https://id.example.com, client_id: x, role_attribute_strict: true}
`, "needs role_attribute_path"},
		"github login instead of id": {`
auth:
  providers:
    - {id: gh, type: github, client_id: x, github_roles: {admin: [gempir]}}
`, "numeric GitHub user id"},
		"id in two roles": {`
auth:
  providers:
    - {id: gh, type: github, client_id: x, github_roles: {admin: [1], disabled: [1]}}
`, "listed under both"},
		"unknown default role": {`
auth:
  default_role: viewer
`, "unknown role"},
		"unknown github role": {`
auth:
  providers:
    - {id: gh, type: github, client_id: x, github_roles: {editor: [1]}}
`, "unknown role"},
		"negative quota": {`
auth:
  user_max_agents: -1
`, "user_max_agents"},
		"path on github": {`
auth:
  providers:
    - {id: gh, type: github, client_id: x, role_attribute_path: "'Admin'"}
`, "are for oidc providers"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadYAML(t, "public_url: https://a.example.com\n"+tc.yaml)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
