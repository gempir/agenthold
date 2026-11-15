// Package config loads the server configuration from a YAML file and the environment.
//
// The file path comes from AGENTHOLD_CONFIG. ${VAR} references inside the file
// are expanded from the environment, so secrets can stay out of the file.
// A few common settings can also be overridden directly by environment variables.
package config

import (
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/gempir/agenthold/internal/password"
)

// Config is the complete server configuration.
type Config struct {
	Listen    string `yaml:"listen"`
	PublicURL string `yaml:"public_url"`
	DataDir   string `yaml:"data_dir"`
	// DevProxy is the Vite dev server URL used in development.
	DevProxy string `yaml:"dev_proxy"`
	// TestHarness offers the scripted test harness (development and tests;
	// its binary is not in the production image).
	TestHarness bool    `yaml:"test_harness"`
	Auth        Auth    `yaml:"auth"`
	Sandbox     Sandbox `yaml:"sandbox"`
	Egress      Egress  `yaml:"egress"`
	Secrets     Secrets `yaml:"secrets"`
}

// Egress configures the proxy that is the sandboxes' only way out.
type Egress struct {
	// Disabled turns interception off: sandboxes then have no network access at all.
	Disabled bool `yaml:"disabled"`
	// Ports the node listens on.
	ProxyPort    int `yaml:"proxy_port"`    // transparent, receives redirected TCP
	ExplicitPort int `yaml:"explicit_port"` // HTTP proxy for HTTPS_PROXY
	DNSPort      int `yaml:"dns_port"`      // receives redirected DNS
	// DNSUpstream is the resolver used for sandboxes' queries. Default: the
	// first nameserver in the server's /etc/resolv.conf.
	DNSUpstream string `yaml:"dns_upstream"`
}

// Secrets configures secret storage.
type Secrets struct {
	// MasterKeyFile holds the base64 master key. AGENTHOLD_MASTER_KEY takes
	// precedence. Default <data_dir>/master.key, created on first start.
	MasterKeyFile string `yaml:"master_key_file"`
}

// Sandbox configures agent sandboxes.
type Sandbox struct {
	// Driver is "firecracker" (default) or "fake" (in-process, for UI development).
	Driver string `yaml:"driver"`
	// Defaults and upper bounds for agent resources.
	DefaultCPUs     float64     `yaml:"default_cpus"`
	DefaultMemoryMB int64       `yaml:"default_memory_mb"`
	MaxCPUs         float64     `yaml:"max_cpus"`
	MaxMemoryMB     int64       `yaml:"max_memory_mb"`
	Firecracker     Firecracker `yaml:"firecracker"`
	// NetworkPool is divided into isolated /30 networks, one per agent.
	NetworkPool string `yaml:"network_pool"`
	// FakeDir is where the fake driver keeps sandbox home directories.
	FakeDir string `yaml:"fake_dir"`
	// TunnelListen is the address sandboxd connects to (only reachable from
	// agent networks in a normal deployment). Default ":8090".
	TunnelListen string `yaml:"tunnel_listen"`
	// IdleCheckInterval is how often idle agents are looked for. Default 30s.
	IdleCheckInterval time.Duration `yaml:"idle_check_interval"`
	// SandboxdPath is the Linux sandboxd binary handed to sandboxes. Default:
	// "sandboxd" next to the server binary, else /usr/local/lib/agenthold/sandboxd.
	SandboxdPath string `yaml:"sandboxd_path"`
}

// Firecracker configures the microVM runtime. Release builds extract the
// runtime artifacts automatically; explicit paths support development builds.
type Firecracker struct {
	Binary    string `yaml:"binary"`
	Jailer    string `yaml:"jailer"`
	Kernel    string `yaml:"kernel"`
	BaseImage string `yaml:"base_image"`
	OverlayGB int64  `yaml:"overlay_gb"`
	HomeGB    int64  `yaml:"home_gb"`
}

// Auth configures sign-in.
type Auth struct {
	Providers []Provider `yaml:"providers"`
	// Local is the one local admin who signs in with username and password.
	Local *Local `yaml:"local"`
	// SessionTTL is how long a browser session lasts without use. Default 30 days.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

// Local is the local admin account.
type Local struct {
	Username string `yaml:"username"`
	// PasswordHash is an argon2id hash from `agenthold hash-password`.
	PasswordHash string `yaml:"password_hash"`
}

// LocalProviderID is the identity provider id of the local admin. Configured
// providers cannot use it.
const LocalProviderID = "local"

// SignInConfigured reports whether anybody can sign in.
func (a Auth) SignInConfigured() bool { return a.Local != nil || len(a.Providers) > 0 }

// ProviderType is the kind of identity provider.
type ProviderType string

// Provider types.
const (
	ProviderOIDC   ProviderType = "oidc"
	ProviderGitHub ProviderType = "github"
)

// Provider configures one identity provider.
type Provider struct {
	// ID is used in URLs (/api/v1/auth/login/{id}) and stored with each identity.
	// Changing it detaches existing users from this provider.
	ID           string       `yaml:"id"`
	Type         ProviderType `yaml:"type"`
	Name         string       `yaml:"name"`
	ClientID     string       `yaml:"client_id"`
	ClientSecret string       `yaml:"client_secret"`
	Scopes       []string     `yaml:"scopes"`

	// OIDC: the issuer the ID tokens carry.
	Issuer string `yaml:"issuer"`
	// OIDC: optional URL the server uses to fetch discovery, when the issuer
	// is not reachable from the server under its public name (for example
	// Keycloak in the dev compose stack).
	DiscoveryURL string `yaml:"discovery_url"`

	// GitHub: overrides for GitHub Enterprise Server. Defaults to github.com.
	GitHubURL    string `yaml:"github_url"`
	GitHubAPIURL string `yaml:"github_api_url"`
	// GitHubAllowedIDs optionally restricts sign-in to these immutable GitHub user IDs.
	// An empty list allows any GitHub account to sign in.
	GitHubAllowedIDs []string `yaml:"github_allowed_ids"`
}

// Load reads the config file named by AGENTHOLD_CONFIG (optional) and applies
// environment overrides and defaults.
func Load() (*Config, error) {
	cfg := &Config{}
	if path := os.Getenv("AGENTHOLD_CONFIG"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := Parse(data, cfg); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	applyEnv(cfg)
	ApplyDefaults(cfg)
	return cfg, cfg.Validate()
}

// envRef matches ${VAR}, ${VAR:-default} and the escape $${...}.
var envRef = regexp.MustCompile(`\$?\$\{([^}]*)\}`)

// Parse decodes YAML into cfg after expanding ${VAR} references. Only the
// braced form is expanded, so values like argon2id hashes keep their "$"
// characters; write $${VAR} for a literal ${VAR}.
func Parse(data []byte, cfg *Config) error {
	expanded := envRef.ReplaceAllStringFunc(string(data), func(m string) string {
		if strings.HasPrefix(m, "$$") {
			return m[1:]
		}
		k := m[2 : len(m)-1]
		// ${VAR:-default}
		if name, def, ok := strings.Cut(k, ":-"); ok {
			if v := os.Getenv(name); v != "" {
				return v
			}
			return def
		}
		return os.Getenv(k)
	})
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

func applyEnv(cfg *Config) {
	set := func(dst *string, key string) {
		if v := os.Getenv(key); v != "" {
			*dst = v
		}
	}
	set(&cfg.Listen, "AGENTHOLD_LISTEN")
	set(&cfg.PublicURL, "AGENTHOLD_PUBLIC_URL")
	set(&cfg.DataDir, "AGENTHOLD_DATA_DIR")
	set(&cfg.DevProxy, "AGENTHOLD_DEV_PROXY")
	if cfg.DevProxy == "off" { // serve the embedded build even if the file sets a dev proxy
		cfg.DevProxy = ""
	}
}

// ApplyDefaults fills unset fields with defaults.
func ApplyDefaults(cfg *Config) {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = "http://localhost:8080"
	}
	cfg.PublicURL = strings.TrimRight(cfg.PublicURL, "/")
	if cfg.DataDir == "" {
		cfg.DataDir = "data"
	}
	sb := &cfg.Sandbox
	if sb.Driver == "" {
		sb.Driver = "firecracker"
	}
	if sb.DefaultCPUs == 0 {
		sb.DefaultCPUs = 2
	}
	if sb.DefaultMemoryMB == 0 {
		sb.DefaultMemoryMB = 4096
	}
	if sb.MaxCPUs == 0 {
		sb.MaxCPUs = 8
	}
	if sb.MaxMemoryMB == 0 {
		sb.MaxMemoryMB = 16384
	}
	if sb.FakeDir == "" {
		sb.FakeDir = filepath.Join(cfg.DataDir, "fake-sandboxes")
	}
	if sb.NetworkPool == "" {
		sb.NetworkPool = "10.213.0.0/16"
	}
	fc := &sb.Firecracker
	if fc.Binary == "" {
		fc.Binary = filepath.Join(cfg.DataDir, "runtime", "firecracker")
	}
	if fc.Jailer == "" {
		fc.Jailer = filepath.Join(cfg.DataDir, "runtime", "jailer")
	}
	if fc.Kernel == "" {
		fc.Kernel = filepath.Join(cfg.DataDir, "runtime", "vmlinux")
	}
	if fc.BaseImage == "" {
		fc.BaseImage = filepath.Join(cfg.DataDir, "base", "base.ext4")
	}
	if fc.OverlayGB == 0 {
		fc.OverlayGB = 8
	}
	if fc.HomeGB == 0 {
		fc.HomeGB = 10
	}
	if sb.IdleCheckInterval == 0 {
		sb.IdleCheckInterval = 30 * time.Second
	}
	if sb.TunnelListen == "" {
		sb.TunnelListen = ":8090"
	}
	if sb.SandboxdPath == "" {
		sb.SandboxdPath = "/usr/local/lib/agenthold/sandboxd"
		if exe, err := os.Executable(); err == nil {
			if p := filepath.Join(filepath.Dir(exe), "sandboxd"); fileExists(p) {
				sb.SandboxdPath = p
			}
		}
	}
	eg := &cfg.Egress
	if eg.ProxyPort == 0 {
		eg.ProxyPort = 15001
	}
	if eg.ExplicitPort == 0 {
		eg.ExplicitPort = 15002
	}
	if eg.DNSPort == 0 {
		eg.DNSPort = 15053
	}
	if cfg.Secrets.MasterKeyFile == "" {
		cfg.Secrets.MasterKeyFile = filepath.Join(cfg.DataDir, "master.key")
	}
	if cfg.Auth.SessionTTL == 0 {
		cfg.Auth.SessionTTL = 30 * 24 * time.Hour
	}
	for i := range cfg.Auth.Providers {
		p := &cfg.Auth.Providers[i]
		if p.Name == "" {
			p.Name = p.ID
		}
		if len(p.Scopes) == 0 {
			switch p.Type {
			case ProviderOIDC:
				p.Scopes = []string{"openid", "profile", "email"}
			case ProviderGitHub:
				p.Scopes = []string{"read:user", "user:email"}
			}
		}
		if p.Type == ProviderGitHub {
			if p.GitHubURL == "" {
				p.GitHubURL = "https://github.com"
			}
			if p.GitHubAPIURL == "" {
				p.GitHubAPIURL = "https://api.github.com"
			}
		}
	}
}

// Validate reports configuration errors.
func (c *Config) Validate() error {
	var errs []error
	if _, err := url.Parse(c.PublicURL); err != nil {
		errs = append(errs, fmt.Errorf("public_url: %w", err))
	}
	switch c.Sandbox.Driver {
	case "firecracker", "fake":
	default:
		errs = append(errs, fmt.Errorf("sandbox.driver: unknown driver %q (want firecracker or fake)", c.Sandbox.Driver))
	}
	if p, err := netip.ParsePrefix(c.Sandbox.NetworkPool); err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 || p.Masked() != p {
		errs = append(errs, fmt.Errorf("sandbox.network_pool: want an aligned IPv4 CIDR between /8 and /30, got %q", c.Sandbox.NetworkPool))
	}
	if c.Sandbox.Firecracker.OverlayGB < 1 || c.Sandbox.Firecracker.HomeGB < 1 {
		errs = append(errs, errors.New("sandbox.firecracker: overlay_gb and home_gb must be positive"))
	}
	seen := map[string]bool{}
	for i, p := range c.Auth.Providers {
		prefix := fmt.Sprintf("auth.providers[%d]", i)
		if p.ID == "" || strings.ContainsAny(p.ID, "/?#") {
			errs = append(errs, fmt.Errorf("%s: id must be a non-empty URL path segment", prefix))
		}
		if p.ID == LocalProviderID {
			errs = append(errs, fmt.Errorf("%s: id %q is reserved for auth.local", prefix, p.ID))
		}
		if seen[p.ID] {
			errs = append(errs, fmt.Errorf("%s: duplicate id %q", prefix, p.ID))
		}
		seen[p.ID] = true
		if p.ClientID == "" {
			errs = append(errs, fmt.Errorf("%s: client_id is required", prefix))
		}
		switch p.Type {
		case ProviderOIDC:
			if p.Issuer == "" {
				errs = append(errs, fmt.Errorf("%s: issuer is required for oidc", prefix))
			}
		case ProviderGitHub:
		default:
			errs = append(errs, fmt.Errorf("%s: unknown type %q (want oidc or github)", prefix, p.Type))
		}
	}
	if l := c.Auth.Local; l != nil {
		if strings.TrimSpace(l.Username) == "" || len(l.Username) > 256 {
			errs = append(errs, errors.New("auth.local.username: must be 1 to 256 characters"))
		}
		if _, err := password.Parse(l.PasswordHash); err != nil {
			errs = append(errs, fmt.Errorf("auth.local.password_hash: %w", err))
		}
	}
	return errors.Join(errs...)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
