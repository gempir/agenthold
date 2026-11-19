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
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
	"golang.org/x/net/publicsuffix"

	"github.com/gempir/agenthold/internal/password"
)

// Config is the complete server configuration.
type Config struct {
	Listen    string   `yaml:"listen"`
	PublicURL string   `yaml:"public_url"`
	DataDir   string   `yaml:"data_dir"`
	Database  Database `yaml:"database"`
	// DevProxy is the Vite dev server URL used in development.
	DevProxy string `yaml:"dev_proxy"`
	// TestHarness offers the scripted test harness (development and tests;
	// its binary is not in the production image).
	TestHarness bool    `yaml:"test_harness"`
	Auth        Auth    `yaml:"auth"`
	Sandbox     Sandbox `yaml:"sandbox"`
	Egress      Egress  `yaml:"egress"`
	Secrets     Secrets `yaml:"secrets"`
	// Nodes configures how edge nodes reach this controller.
	Nodes Nodes `yaml:"nodes"`
	// Node configures `agenthold node` (an edge node).
	Node Node `yaml:"node"`
	// Preview configures preview URLs for ports exposed by machines.
	Preview Preview `yaml:"preview"`
	// Apps limits the apps agents create with deploy_app.
	Apps Apps `yaml:"apps"`
}

// Apps limits apps created by agents (users creating apps get the sandbox limits).
type Apps struct {
	// MaxPerAgent is how many apps one agent may have created. Default 3;
	// 0 means agents cannot create apps.
	MaxPerAgent *int `yaml:"max_per_agent"`
	// MaxPerOwner is how many apps one user may own, however they were
	// created. Default 10.
	MaxPerOwner *int `yaml:"max_per_owner"`
	// Resources of an app an agent creates unless it asks for others, and the
	// most it may ask for. Defaults 1 vCPU / 1024 MB and 2 vCPUs / 2048 MB.
	DefaultCPUs     float64 `yaml:"default_cpus"`
	DefaultMemoryMB int64   `yaml:"default_memory_mb"`
	MaxCPUs         float64 `yaml:"max_cpus"`
	MaxMemoryMB     int64   `yaml:"max_memory_mb"`
	// DeployMaxBytes bounds the directory a deploy copies. Default 2 GiB.
	DeployMaxBytes int64 `yaml:"deploy_max_bytes"`
}

// Preview configures exposure (preview URLs).
type Preview struct {
	// HostSuffix turns preview URLs on: an exposure named "web" is served at
	// "web" + HostSuffix. It starts with "." (".apps.example.com": one
	// wildcard subdomain level) or "-" ("-demo.example.com": names share the
	// first label, so a one-level wildcard certificate of the parent domain
	// covers them). Empty turns the feature off.
	HostSuffix string `yaml:"host_suffix"`
}

// MaxExposureName is the longest exposure name (one DNS label part).
const MaxExposureName = 40

// ValidateHostSuffix checks preview.host_suffix against the main host.
func ValidateHostSuffix(suffix, mainHost string) error {
	if suffix == "" {
		return nil
	}
	if suffix != strings.ToLower(suffix) {
		return errors.New("must be lowercase")
	}
	if suffix[0] != '.' && suffix[0] != '-' {
		return errors.New(`must start with "." or "-"`)
	}
	rest := suffix[1:]
	if suffix[0] == '-' && !strings.Contains(rest, ".") {
		return errors.New(`a "-" suffix needs a domain after the dash, like "-demo.example.com"`)
	}
	labels := strings.Split(rest, ".")
	for _, l := range labels {
		if !validLabel(l) {
			return fmt.Errorf("%q is not a valid hostname label", l)
		}
	}
	if suffix[0] == '-' && MaxExposureName+1+len(labels[0]) > 63 {
		return fmt.Errorf("the first label %q is too long: names plus it must fit in 63 characters", labels[0])
	}
	if len(rest) > 200 {
		return errors.New("too long")
	}
	// The domain exposure hosts live under must be one you control: with a
	// public suffix ("-x.dev", ".co.uk") there is no wildcard to point at.
	parent := rest
	if suffix[0] == '-' {
		_, parent, _ = strings.Cut(rest, ".")
	}
	// (Unlisted single-label names such as "localhost" are allowed for development.)
	if ps, icann := publicsuffix.PublicSuffix(parent); ps == parent && (icann || strings.Contains(parent, ".")) {
		return fmt.Errorf("exposure hosts would sit directly under the public suffix %q; use a domain you own", parent)
	}
	h := strings.ToLower(mainHost)
	if h != "" && strings.HasSuffix(h, suffix) {
		return fmt.Errorf("the main host %q must not end with it", mainHost)
	}
	// Exposures below the main host ("web.agents.example.com" for main host
	// "agents.example.com") could set cookies with Domain=<main host>, which
	// browsers send to the main host. Keep them beside it instead. (A main
	// host that is itself a public suffix, such as "localhost", cannot be a
	// cookie domain.)
	if ps, _ := publicsuffix.PublicSuffix(h); h != "" && ps != h && suffix[0] == '.' && (rest == h || strings.HasSuffix(rest, "."+h)) {
		return fmt.Errorf("exposure hosts must not be subdomains of the main host %q", mainHost)
	}
	return nil
}

// PreviewSameSite reports whether exposure hosts share a registrable domain
// (a "site") with the main host. Then they are isolated from it only by the
// host-only cookies and checks agenthold makes, not by the browser's
// same-site rules.
func PreviewSameSite(suffix, mainHost string) bool {
	if suffix == "" || mainHost == "" || net.ParseIP(mainHost) != nil {
		return false
	}
	a, err1 := publicsuffix.EffectiveTLDPlusOne(strings.ToLower(mainHost))
	b, err2 := publicsuffix.EffectiveTLDPlusOne("x" + suffix)
	return err1 == nil && err2 == nil && a == b
}

// validLabel checks a DNS label (RFC 1123).
func validLabel(l string) bool {
	if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return true
}

// Nodes configures the controller's listener for edge nodes.
type Nodes struct {
	// Listen is the TLS listener edge nodes dial. Default ":8091"; "off"
	// disables edge nodes. It is not HTTP and must not sit behind a proxy that
	// terminates TLS: nodes pin the controller's own certificate.
	Listen string `yaml:"listen"`
	// Advertise is the host:port written into join tokens. Default: the
	// public_url host with the listen port.
	Advertise string `yaml:"advertise"`
}

// NodesDisabled is the value of nodes.listen that turns edge nodes off.
const NodesDisabled = "off"

// Node configures an edge node.
type Node struct {
	// Name is proposed to the controller when joining. Default: the hostname.
	Name string `yaml:"name"`
	// StateFile holds the node's identity and pinned controller. Default
	// <data_dir>/node/state.json.
	StateFile string `yaml:"state_file"`
}

// Database selects SQLite (the default) or an operator-managed PostgreSQL database.
type Database struct {
	// URL is sqlite:<path>, postgres://..., or postgresql://... .
	URL string `yaml:"url"`
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

// MinMemoryMB is the smallest guest memory that can run an agent harness;
// smaller guests are OOM-killed. API validation, config validation and the
// start check all use it.
const MinMemoryMB = 1024

// Sandbox configures agent sandboxes.
type Sandbox struct {
	// Driver selects the built-in local node: "auto" (default: firecracker
	// when /dev/kvm is present, otherwise no local node), "firecracker",
	// "none" (agents only run on edge nodes) or "fake" (in-process, for UI
	// development and tests).
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
	// BaseURL is where a release build fetches its base image from when it is not on disk yet:
	// the /dl/ root of another agenthold (https://demo.agenthold.dev/dl/, ending in "/") or the
	// URL of the compressed image itself. The image is checked against checksums embedded in the
	// binary, so the source needs no trust. Default: the controller an edge node joined, else
	// the URL in the release. AGENTHOLD_FIRECRACKER_BASE_URL overrides it.
	BaseURL   string `yaml:"base_url"`
	OverlayGB int64  `yaml:"overlay_gb"`
	HomeGB    int64  `yaml:"home_gb"`
	// Snapshots: sleep keeps a machine's memory (a Firecracker snapshot next
	// to its disks) and wake resumes its processes. Default true; false makes
	// sleep a shutdown and wake a boot.
	Snapshots *bool `yaml:"snapshots"`
	// SnapshotMaxGB caps the disk space all snapshots of this node take
	// (default 0: no cap). Beyond it a machine sleeps without its memory.
	SnapshotMaxGB int64 `yaml:"snapshot_max_gb"`
	// SnapshotReserveGB is the free space a snapshot must leave on the data
	// filesystem (default 2).
	SnapshotReserveGB int64 `yaml:"snapshot_reserve_gb"`
	// Balloon inflates a virtio balloon before a snapshot so the guest's
	// free memory is not written. Default true.
	Balloon *bool `yaml:"balloon"`
}

// SnapshotsEnabled reports whether sleep keeps memory.
func (f Firecracker) SnapshotsEnabled() bool { return f.Snapshots == nil || *f.Snapshots }

// BalloonEnabled reports whether VMs get a balloon for snapshots.
func (f Firecracker) BalloonEnabled() bool { return f.Balloon == nil || *f.Balloon }

// Auth configures sign-in.
type Auth struct {
	Providers []Provider `yaml:"providers"`
	// Local is the one local admin who signs in with username and password.
	Local *Local `yaml:"local"`
	// SessionTTL is how long a browser session lasts without use. Default 30 days.
	SessionTTL time.Duration `yaml:"session_ttl"`
	// Admins are the verified e-mail addresses of users who manage the
	// installation (edge nodes). The local admin (auth.local) always is one.
	Admins []string `yaml:"admins"`
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
	set(&cfg.Database.URL, "AGENTHOLD_DATABASE_URL")
	set(&cfg.DevProxy, "AGENTHOLD_DEV_PROXY")
	set(&cfg.Preview.HostSuffix, "AGENTHOLD_PREVIEW_HOST_SUFFIX")
	set(&cfg.Sandbox.Firecracker.BaseURL, "AGENTHOLD_FIRECRACKER_BASE_URL")
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
	if cfg.Database.URL == "" {
		cfg.Database.URL = "sqlite:" + filepath.Join(cfg.DataDir, "agenthold.db")
	}

	ap := &cfg.Apps
	if ap.MaxPerAgent == nil {
		ap.MaxPerAgent = intPtr(3)
	}
	if ap.MaxPerOwner == nil {
		ap.MaxPerOwner = intPtr(10)
	}
	if ap.DefaultCPUs == 0 {
		ap.DefaultCPUs = 1
	}
	if ap.DefaultMemoryMB == 0 {
		ap.DefaultMemoryMB = MinMemoryMB
	}
	if ap.MaxCPUs == 0 {
		ap.MaxCPUs = 2
	}
	if ap.MaxMemoryMB == 0 {
		ap.MaxMemoryMB = 2048
	}
	if ap.DeployMaxBytes == 0 {
		ap.DeployMaxBytes = 2 << 30
	}

	sb := &cfg.Sandbox
	if sb.Driver == "" {
		sb.Driver = "auto"
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
	if fc.SnapshotReserveGB == 0 {
		fc.SnapshotReserveGB = 2
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
	if cfg.Nodes.Listen == "" {
		cfg.Nodes.Listen = ":8091"
	}
	if cfg.Nodes.Advertise == "" && cfg.Nodes.Listen != NodesDisabled {
		if u, err := url.Parse(cfg.PublicURL); err == nil && u.Hostname() != "" {
			if _, port, err := net.SplitHostPort(cfg.Nodes.Listen); err == nil {
				cfg.Nodes.Advertise = net.JoinHostPort(u.Hostname(), port)
			}
		}
	}
	if cfg.Node.StateFile == "" {
		cfg.Node.StateFile = filepath.Join(cfg.DataDir, "node", "state.json")
	}
	if cfg.Node.Name == "" {
		cfg.Node.Name, _ = os.Hostname()
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
	sb := c.Sandbox
	if sb.DefaultMemoryMB < MinMemoryMB || sb.MaxMemoryMB < MinMemoryMB {
		errs = append(errs, fmt.Errorf("sandbox.default_memory_mb and max_memory_mb: must be at least %d", MinMemoryMB))
	} else if sb.DefaultMemoryMB > sb.MaxMemoryMB {
		errs = append(errs, errors.New("sandbox.default_memory_mb: must not exceed max_memory_mb"))
	}
	if sb.DefaultCPUs < 1 || sb.MaxCPUs < 1 {
		errs = append(errs, errors.New("sandbox.default_cpus and max_cpus: must be at least 1"))
	} else if sb.DefaultCPUs > sb.MaxCPUs {
		errs = append(errs, errors.New("sandbox.default_cpus: must not exceed max_cpus"))
	}
	if ap := c.Apps; ap.MaxPerAgent == nil || *ap.MaxPerAgent < 0 || ap.MaxPerOwner == nil || *ap.MaxPerOwner < 0 || ap.DeployMaxBytes < 0 {
		errs = append(errs, errors.New("apps.max_per_agent, apps.max_per_owner and apps.deploy_max_bytes: must not be negative"))
	} else if ap.DefaultMemoryMB < MinMemoryMB || ap.DefaultMemoryMB > ap.MaxMemoryMB || ap.MaxMemoryMB > sb.MaxMemoryMB {
		errs = append(errs, fmt.Errorf("apps.default_memory_mb and max_memory_mb: want %d <= default <= max <= sandbox.max_memory_mb", MinMemoryMB))
	} else if ap.DefaultCPUs < 1 || ap.DefaultCPUs > ap.MaxCPUs || ap.MaxCPUs > sb.MaxCPUs {
		errs = append(errs, errors.New("apps.default_cpus and max_cpus: want 1 <= default <= max <= sandbox.max_cpus"))
	}
	databaseURL, parseErr := url.Parse(c.Database.URL)
	if parseErr != nil {
		errs = append(errs, errors.New("database.url: invalid URL"))
	} else {
		switch databaseURL.Scheme {
		case "sqlite":
			if databaseURL.Host != "" || databaseURL.User != nil || databaseURL.Fragment != "" || databaseURL.RawQuery != "" || databaseURL.Opaque == "" && databaseURL.Path == "" {
				errs = append(errs, errors.New("database.url: want sqlite:<path> without query parameters"))
			}
		case "postgres", "postgresql":
			if databaseURL.Opaque != "" || databaseURL.Fragment != "" {
				errs = append(errs, errors.New("database.url: invalid PostgreSQL URL"))
			}
		default:
			errs = append(errs, errors.New("database.url: scheme must be sqlite, postgres, or postgresql"))
		}
	}
	if u, err := url.Parse(c.PublicURL); err != nil {
		errs = append(errs, fmt.Errorf("public_url: %w", err))
	} else if err := ValidateHostSuffix(c.Preview.HostSuffix, u.Hostname()); err != nil {
		errs = append(errs, fmt.Errorf("preview.host_suffix: %w", err))
	}
	switch c.Sandbox.Driver {
	case "auto", "firecracker", "none", "fake":
	default:
		errs = append(errs, fmt.Errorf("sandbox.driver: unknown driver %q (want auto, firecracker, none or fake)", c.Sandbox.Driver))
	}
	if c.Nodes.Listen != NodesDisabled {
		if _, _, err := net.SplitHostPort(c.Nodes.Listen); err != nil {
			errs = append(errs, fmt.Errorf("nodes.listen: want host:port or %q, got %q", NodesDisabled, c.Nodes.Listen))
		}
		if _, _, err := net.SplitHostPort(c.Nodes.Advertise); err != nil {
			errs = append(errs, fmt.Errorf("nodes.advertise: want host:port, got %q", c.Nodes.Advertise))
		}
	}
	for i, a := range c.Auth.Admins {
		if !strings.Contains(a, "@") {
			errs = append(errs, fmt.Errorf("auth.admins[%d]: want an e-mail address, got %q", i, a))
		}
	}
	if p, err := netip.ParsePrefix(c.Sandbox.NetworkPool); err != nil || !p.Addr().Is4() || p.Bits() < 8 || p.Bits() > 30 || p.Masked() != p {
		errs = append(errs, fmt.Errorf("sandbox.network_pool: want an aligned IPv4 CIDR between /8 and /30, got %q", c.Sandbox.NetworkPool))
	}
	if c.Sandbox.Firecracker.OverlayGB < 1 || c.Sandbox.Firecracker.HomeGB < 1 {
		errs = append(errs, errors.New("sandbox.firecracker: overlay_gb and home_gb must be positive"))
	}
	if c.Sandbox.Firecracker.SnapshotMaxGB < 0 || c.Sandbox.Firecracker.SnapshotReserveGB < 0 {
		errs = append(errs, errors.New("sandbox.firecracker: snapshot_max_gb and snapshot_reserve_gb must not be negative"))
	}
	if u := c.Sandbox.Firecracker.BaseURL; u != "" {
		if p, err := url.Parse(u); err != nil || p.Host == "" || p.User != nil || p.Fragment != "" || (p.Scheme != "https" && p.Scheme != "http") {
			errs = append(errs, fmt.Errorf("sandbox.firecracker.base_url: want an absolute HTTP(S) URL without credentials, got %q", u))
		}
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

// IsAdminEmail reports whether a verified e-mail address is in auth.admins.
func (a Auth) IsAdminEmail(email string) bool {
	for _, admin := range a.Admins {
		if email != "" && strings.EqualFold(strings.TrimSpace(admin), email) {
			return true
		}
	}
	return false
}

// LocalDriver resolves sandbox.driver "auto" for this host: firecracker when
// kvmAvailable, otherwise "none".
func (s Sandbox) LocalDriver(kvmAvailable bool) string {
	if s.Driver != "auto" {
		return s.Driver
	}
	if kvmAvailable {
		return "firecracker"
	}
	return "none"
}

func intPtr(n int) *int { return &n }
