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
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
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
	// Ports the server listens on (inside its own container).
	ProxyPort    int `yaml:"proxy_port"`    // transparent, receives redirected TCP
	ExplicitPort int `yaml:"explicit_port"` // HTTP proxy for HTTPS_PROXY
	DNSPort      int `yaml:"dns_port"`      // receives redirected DNS
	// DNSUpstream is the resolver used for sandboxes' queries. Default: the
	// first nameserver in the server's /etc/resolv.conf.
	DNSUpstream string `yaml:"dns_upstream"`
	// LogKeep is how many request log entries are kept per agent.
	LogKeep int `yaml:"log_keep"`
}

// Secrets configures secret storage.
type Secrets struct {
	// MasterKeyFile holds the base64 master key. AGENTHOLD_MASTER_KEY takes
	// precedence. Default <data_dir>/master.key, created on first start.
	MasterKeyFile string `yaml:"master_key_file"`
}

// Sandbox configures agent sandboxes.
type Sandbox struct {
	// Driver is "docker" (default) or "fake" (in-process, for development without Docker).
	Driver string `yaml:"driver"`
	// DefaultImage is used when an agent does not name an image.
	DefaultImage string `yaml:"default_image"`
	// Defaults and upper bounds for agent resources.
	DefaultCPUs     float64 `yaml:"default_cpus"`
	DefaultMemoryMB int64   `yaml:"default_memory_mb"`
	MaxCPUs         float64 `yaml:"max_cpus"`
	MaxMemoryMB     int64   `yaml:"max_memory_mb"`
	PIDsLimit       int64   `yaml:"pids_limit"`
	Docker          Docker  `yaml:"docker"`
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

// Docker configures the Docker sandbox driver.
type Docker struct {
	// Host overrides DOCKER_HOST.
	Host string `yaml:"host"`
	// NamePrefix prefixes all containers, networks and volumes. Change it to
	// run several installations on one Docker host.
	NamePrefix string `yaml:"name_prefix"`
	// NetworkPool is the address range per-agent networks are carved from.
	NetworkPool string `yaml:"network_pool"`
	// SubnetBits is the prefix length of each agent network (default 28: 14 hosts).
	SubnetBits int `yaml:"subnet_bits"`
	// Runtime is an optional OCI runtime, e.g. "sysbox-runc" or "runsc".
	Runtime string `yaml:"runtime"`
	// ServerContainer is the server's own container, which is attached to every
	// agent network. Empty means autodetect when running in a container.
	ServerContainer string `yaml:"server_container"`
	// SandboxdVolume is the Docker volume that carries sandboxd into sandboxes
	// (mounted read-only at /opt/agenthold). Default "<name_prefix>-bin".
	SandboxdVolume string `yaml:"sandboxd_volume"`
	// SandboxdVolumeDir is where that volume is mounted inside the server's
	// container; the server copies sandboxd there at startup. Required when
	// the server runs in a container. When the server runs on the host,
	// sandboxd_path's directory is bind-mounted instead.
	SandboxdVolumeDir string `yaml:"sandboxd_volume_dir"`
	// TunnelHost overrides the address sandboxes dial to reach the server.
	TunnelHost string `yaml:"tunnel_host"`
}

// Auth configures sign-in.
type Auth struct {
	Providers []Provider `yaml:"providers"`
	// SessionTTL is how long a browser session lasts without use. Default 30 days.
	SessionTTL time.Duration `yaml:"session_ttl"`
}

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

// Parse decodes YAML into cfg after expanding ${VAR} references.
func Parse(data []byte, cfg *Config) error {
	expanded := os.Expand(string(data), func(k string) string {
		if k == "$" {
			return "$"
		}
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
		sb.Driver = "docker"
	}
	if sb.DefaultImage == "" {
		sb.DefaultImage = "debian:bookworm-slim"
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
	if sb.PIDsLimit == 0 {
		sb.PIDsLimit = 4096
	}
	if sb.FakeDir == "" {
		sb.FakeDir = filepath.Join(cfg.DataDir, "fake-sandboxes")
	}
	if sb.Docker.NamePrefix == "" {
		sb.Docker.NamePrefix = "agenthold"
	}
	if sb.Docker.SandboxdVolume == "" {
		sb.Docker.SandboxdVolume = sb.Docker.NamePrefix + "-bin"
	}
	if sb.Docker.SandboxdVolumeDir == "" && fileExists("/.dockerenv") {
		// In a container the server cannot bind-mount its own files into
		// sandboxes; it shares sandboxd through a volume mounted here.
		sb.Docker.SandboxdVolumeDir = "/var/lib/agenthold/bin"
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
	if sb.Docker.NetworkPool == "" {
		sb.Docker.NetworkPool = "10.213.0.0/16"
	}
	if sb.Docker.SubnetBits == 0 {
		sb.Docker.SubnetBits = 28
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
	if eg.LogKeep == 0 {
		eg.LogKeep = 5000
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
	case "docker", "fake":
	default:
		errs = append(errs, fmt.Errorf("sandbox.driver: unknown driver %q (want docker or fake)", c.Sandbox.Driver))
	}
	if p, err := netip.ParsePrefix(c.Sandbox.Docker.NetworkPool); err != nil || !p.Addr().Is4() {
		errs = append(errs, fmt.Errorf("sandbox.docker.network_pool: want an IPv4 CIDR, got %q", c.Sandbox.Docker.NetworkPool))
	} else if b := c.Sandbox.Docker.SubnetBits; b < p.Bits() || b > 29 {
		errs = append(errs, fmt.Errorf("sandbox.docker.subnet_bits: must be between %d and 29", p.Bits()))
	}
	seen := map[string]bool{}
	for i, p := range c.Auth.Providers {
		prefix := fmt.Sprintf("auth.providers[%d]", i)
		if p.ID == "" || strings.ContainsAny(p.ID, "/?#") {
			errs = append(errs, fmt.Errorf("%s: id must be a non-empty URL path segment", prefix))
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
	return errors.Join(errs...)
}

func fileExists(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
