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
		{"default memory below floor", "sandbox: {default_memory_mb: 128}", "at least 1024"},
		{"max memory below floor", "sandbox: {default_memory_mb: 1024, max_memory_mb: 512}", "at least 1024"},
		{"default memory above max", "sandbox: {default_memory_mb: 8192, max_memory_mb: 4096}", "must not exceed max_memory_mb"},
		{"default cpus below one", "sandbox: {default_cpus: 0.5}", "at least 1"},
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

func TestDatabaseConfiguration(t *testing.T) {
	cfg := &Config{DataDir: "/srv/agenthold"}
	ApplyDefaults(cfg)
	if cfg.Database.URL != "sqlite:/srv/agenthold/agenthold.db" {
		t.Fatal(cfg.Database.URL)
	}
	for _, location := range []string{"sqlite::memory:", "sqlite:relative.db", "sqlite:///tmp/agenthold.db", "postgres://alice@localhost/agenthold?sslmode=require", "postgresql:///agenthold?host=/run/postgresql"} {
		cfg.Database.URL = location
		if err := cfg.Validate(); err != nil {
			t.Errorf("%s: %v", location, err)
		}
	}
	for _, location := range []string{"mysql://localhost/agenthold", "sqlite:", "sqlite://hostname/file.db", "sqlite:file.db?foreign_keys=off", "postgres:invalid", ""} {
		cfg.Database.URL = location
		if err := cfg.Validate(); err == nil {
			t.Errorf("accepted %q", location)
		}
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(file, []byte("data_dir: /srv/agenthold\ndatabase:\n  url: sqlite:configured.db\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHOLD_CONFIG", file)
	t.Setenv("AGENTHOLD_DATABASE_URL", "postgres://localhost/overridden?sslmode=disable")
	loaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Database.URL != "postgres://localhost/overridden?sslmode=disable" {
		t.Fatal(loaded.Database.URL)
	}
}

func TestValidateHostSuffix(t *testing.T) {
	for _, tc := range []struct {
		suffix, main string
		ok           bool
	}{
		{"", "agents.example.com", true},
		{".apps.example.com", "agents.example.com", true},
		{"-demo.agenthold.dev", "demo.agenthold.dev", true},
		{".localhost", "localhost", true},
		{"apps.example.com", "x", false},              // no leading . or -
		{".Apps.example.com", "x", false},             // uppercase
		{"-example", "x", false},                      // "-" needs a domain
		{".apps..example.com", "x", false},            // empty label
		{".apps.example.com.", "x", false},            // trailing dot
		{".-apps.example.com", "x", false},            // label starts with -
		{".apps_x.example.com", "x", false},           // underscore
		{".example.com", "agents.example.com", false}, // main host would match
		{"-demo.agenthold.dev", "x-demo.agenthold.dev", false},
		{"-" + strings.Repeat("a", 23) + ".example.dev", "x", false}, // label too long for 40-char names
		{"-" + strings.Repeat("a", 22) + ".example.dev", "x", true},
		{".dev", "x", false}, // a public suffix
		{".co.uk", "x", false},
		{"-x.dev", "x", false}, // web-x.dev is its own registrable domain
		{".github.io", "x", false},
		{".demo.agenthold.dev", "demo.agenthold.dev", false}, // below the main host
		{".apps.agents.example.com", "agents.example.com", false},
		{".agents-apps.example.com", "agents.example.com", true},
	} {
		err := ValidateHostSuffix(tc.suffix, tc.main)
		if (err == nil) != tc.ok {
			t.Errorf("ValidateHostSuffix(%q, %q) = %v, want ok=%v", tc.suffix, tc.main, err, tc.ok)
		}
	}
}

func TestPreviewSuffixFromEnv(t *testing.T) {
	t.Setenv("AGENTHOLD_CONFIG", "")
	t.Setenv("AGENTHOLD_PUBLIC_URL", "https://agents.example.com")
	t.Setenv("AGENTHOLD_PREVIEW_HOST_SUFFIX", ".example.com")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "preview.host_suffix") {
		t.Fatalf("err = %v", err)
	}
	t.Setenv("AGENTHOLD_PREVIEW_HOST_SUFFIX", ".apps.example.com")
	cfg, err := Load()
	if err != nil || cfg.Preview.HostSuffix != ".apps.example.com" {
		t.Fatalf("cfg = %+v, err %v", cfg.Preview, err)
	}
}

func TestPreviewSameSite(t *testing.T) {
	for _, tc := range []struct {
		suffix, main string
		want         bool
	}{
		{"-demo.agenthold.dev", "demo.agenthold.dev", true},
		{".apps.example.com", "agents.example.com", true},
		{".example-apps.net", "agents.example.com", false},
		{".apps.test", "127.0.0.1", false},
		{"", "agents.example.com", false},
	} {
		if got := PreviewSameSite(tc.suffix, tc.main); got != tc.want {
			t.Errorf("PreviewSameSite(%q, %q) = %v", tc.suffix, tc.main, got)
		}
	}
}

func TestBaseURL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(path, []byte("sandbox:\n  firecracker:\n    base_url: https://demo.example/dl/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENTHOLD_CONFIG", path)
	cfg, err := Load()
	if err != nil || cfg.Sandbox.Firecracker.BaseURL != "https://demo.example/dl/" {
		t.Fatalf("file: %v %q", err, cfg.Sandbox.Firecracker.BaseURL)
	}
	t.Setenv("AGENTHOLD_FIRECRACKER_BASE_URL", "https://mirror.example/base.zst")
	if cfg, err = Load(); err != nil || cfg.Sandbox.Firecracker.BaseURL != "https://mirror.example/base.zst" {
		t.Fatalf("env override: %v %q", err, cfg.Sandbox.Firecracker.BaseURL)
	}
	for _, bad := range []string{"demo.example/dl/", "ftp://x/", "https://u:p@x.example/dl/", "/local/path"} {
		t.Setenv("AGENTHOLD_FIRECRACKER_BASE_URL", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "base_url") {
			t.Errorf("%q accepted: %v", bad, err)
		}
	}
}
