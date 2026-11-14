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
	"net/url"
	"os"
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
	Auth     Auth   `yaml:"auth"`
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
	applyDefaults(cfg)
	return cfg, cfg.Validate()
}

// Parse decodes YAML into cfg after expanding ${VAR} references.
func Parse(data []byte, cfg *Config) error {
	expanded := os.Expand(string(data), func(k string) string {
		if k == "$" {
			return "$"
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
}

func applyDefaults(cfg *Config) {
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
