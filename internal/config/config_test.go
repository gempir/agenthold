package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gempir/agenthold/internal/password"
)

func TestLoadExpandsEnvAndDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte(`
public_url: https://agents.example.com/
auth:
  providers:
    - id: kc
      type: oidc
      issuer: https://id.example.com/realms/x
      client_id: agenthold
      client_secret: ${KC_SECRET}
    - id: github
      type: github
      client_id: abc
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHOLD_CONFIG", path)
	t.Setenv("KC_SECRET", "s3cret")
	t.Setenv("AGENTHOLD_LISTEN", ":9999")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://agents.example.com" || cfg.Listen != ":9999" || cfg.DataDir != "data" {
		t.Fatalf("cfg = %+v", cfg)
	}
	kc, gh := cfg.Auth.Providers[0], cfg.Auth.Providers[1]
	if kc.ClientSecret != "s3cret" || kc.Name != "kc" || strings.Join(kc.Scopes, " ") != "openid profile email" {
		t.Fatalf("kc = %+v", kc)
	}
	if gh.GitHubURL != "https://github.com" || gh.GitHubAPIURL != "https://api.github.com" {
		t.Fatalf("gh = %+v", gh)
	}
}

func TestExpandDefaults(t *testing.T) {
	t.Setenv("SET_VAR", "from-env")
	cfg := &Config{}
	if err := Parse([]byte("listen: ${UNSET_VAR:-:7000}\npublic_url: ${SET_VAR:-nope}\ndev_proxy: $plain$$x$${SET_VAR}\n"), cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Listen != ":7000" || cfg.PublicURL != "from-env" || cfg.DevProxy != "$plain$$x${SET_VAR}" {
		t.Fatalf("cfg %+v", cfg)
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"unknown field", "listn: x", "field listn not found"},
		{"oidc without issuer", "auth: {providers: [{id: a, type: oidc, client_id: c}]}", "issuer is required"},
		{"bad type", "auth: {providers: [{id: a, type: saml, client_id: c}]}", "unknown type"},
		{"duplicate", "auth: {providers: [{id: a, type: github, client_id: c}, {id: a, type: github, client_id: c}]}", "duplicate id"},
		{"no client id", "auth: {providers: [{id: a, type: github}]}", "client_id is required"},
		{"bad id", "auth: {providers: [{id: a/b, type: github, client_id: c}]}", "path segment"},
		{"reserved id", "auth: {providers: [{id: local, type: github, client_id: c}]}", "reserved"},
		{"local without username", "auth: {local: {password_hash: x}}", "auth.local.username"},
		{"local with plaintext password", "auth: {local: {username: admin, password_hash: hunter2}}", "not an argon2id hash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{}
			err := Parse([]byte(tt.yaml), cfg)
			if err == nil {
				ApplyDefaults(cfg)
				err = cfg.Validate()
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLocalAdmin(t *testing.T) {
	hash, err := password.New("pw")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	if err := Parse([]byte("auth:\n  local:\n    username: admin\n    password_hash: '"+hash+"'\n"), cfg); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Local == nil || cfg.Auth.Local.Username != "admin" || !cfg.Auth.SignInConfigured() {
		t.Fatalf("local = %+v", cfg.Auth.Local)
	}
	if (Auth{}).SignInConfigured() {
		t.Fatal("empty auth reports sign-in configured")
	}
}

// The dev config ships a local admin; it must stay valid.
func TestDevConfigIsValid(t *testing.T) {
	data, err := os.ReadFile("../../deploy/dev/agenthold.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfg := &Config{}
	if err := Parse(data, cfg); err != nil {
		t.Fatal(err)
	}
	ApplyDefaults(cfg)
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if cfg.Auth.Local == nil || cfg.Auth.Local.Username != "admin" {
		t.Fatalf("local admin %+v", cfg.Auth.Local)
	}
	h, err := password.Parse(cfg.Auth.Local.PasswordHash)
	if err != nil || !h.Verify("admin") {
		t.Fatalf("dev admin password is not %q: %v", "admin", err)
	}
}
