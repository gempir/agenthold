package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"text/template"
)

// Rule says where and how a secret is injected.
type Rule struct {
	// Hosts the secret may be sent to: exact names or "*.example.com".
	Hosts []string `json:"hosts"`
	// Header is the request header to set.
	Header string `json:"header"`
	// Template renders the header value; {{.Value}} is the secret.
	// Functions: base64.
	Template string `json:"template"`
}

// Validate checks a rule and compiles its template.
func (r Rule) Validate() error {
	if len(r.Hosts) == 0 {
		return errors.New("at least one host is required")
	}
	for _, h := range r.Hosts {
		if !validHostPattern(h) {
			return fmt.Errorf("invalid host pattern %q", h)
		}
	}
	if r.Header == "" || strings.ContainsAny(r.Header, " :\r\n") {
		return errors.New("header must be a valid header name")
	}
	if _, err := r.compile(); err != nil {
		return fmt.Errorf("template: %w", err)
	}
	return nil
}

func validHostPattern(h string) bool {
	h = strings.TrimPrefix(h, "*.")
	if h == "" || len(h) > 253 || strings.ContainsAny(h, "*/:@ ") {
		return false
	}
	return strings.Contains(h, ".") || h == "localhost"
}

var funcs = template.FuncMap{
	"base64": func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) },
}

func (r Rule) compile() (*template.Template, error) {
	t := r.Template
	if t == "" {
		t = "{{.Value}}"
	}
	return template.New("header").Funcs(funcs).Option("missingkey=error").Parse(t)
}

// Render produces the header value for a secret value.
func (r Rule) Render(value string) (string, error) {
	t, err := r.compile()
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, struct{ Value string }{value}); err != nil {
		return "", err
	}
	out := b.String()
	if strings.ContainsAny(out, "\r\n") {
		return "", errors.New("rendered header contains a line break")
	}
	return out, nil
}

// MatchHost reports whether host matches one of the rule's patterns.
func (r Rule) MatchHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	for _, p := range r.Hosts {
		if MatchPattern(p, host) {
			return true
		}
	}
	return false
}

// MatchPattern matches a host against "name" or "*.name" (which also matches
// deeper subdomains, but not the bare name).
func MatchPattern(pattern, host string) bool {
	pattern = strings.ToLower(pattern)
	if suffix, ok := strings.CutPrefix(pattern, "*."); ok {
		return strings.HasSuffix(host, "."+suffix)
	}
	return host == pattern
}

// Injection is a resolved secret ready for the proxy.
type Injection struct {
	SecretID    string
	Rule        Rule
	Value       string
	Placeholder string
}

// Apply sets the rule's header and replaces the placeholder anywhere in the
// request's headers and URL. It returns whether anything was injected.
func (inj Injection) Apply(req *http.Request) (bool, error) {
	if !inj.Rule.MatchHost(req.URL.Hostname()) {
		return false, nil
	}
	val, err := inj.Rule.Render(inj.Value)
	if err != nil {
		return false, err
	}
	req.Header.Set(inj.Rule.Header, val)
	if inj.Placeholder != "" {
		for k, vs := range req.Header {
			for i, v := range vs {
				if strings.Contains(v, inj.Placeholder) {
					req.Header[k][i] = strings.ReplaceAll(v, inj.Placeholder, inj.Value)
				}
			}
		}
		if strings.Contains(req.URL.RawQuery, inj.Placeholder) {
			req.URL.RawQuery = strings.ReplaceAll(req.URL.RawQuery, inj.Placeholder, inj.Value)
		}
	}
	return true, nil
}

// Preset is a ready-made rule for a common provider.
type Preset struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Rule        Rule   `json:"rule"`
	EnvVar      string `json:"envVar"`
	Description string `json:"description"`
}

// Presets for common providers.
var Presets = []Preset{
	{
		ID: "anthropic", Name: "Anthropic API key", EnvVar: "ANTHROPIC_API_KEY",
		Rule:        Rule{Hosts: []string{"api.anthropic.com"}, Header: "x-api-key", Template: "{{.Value}}"},
		Description: "Claude API key, sent as x-api-key to api.anthropic.com.",
	},
	{
		ID: "openai", Name: "OpenAI API key", EnvVar: "OPENAI_API_KEY",
		Rule:        Rule{Hosts: []string{"api.openai.com"}, Header: "Authorization", Template: "Bearer {{.Value}}"},
		Description: "OpenAI API key, sent as a bearer token to api.openai.com.",
	},
	{
		ID: "github", Name: "GitHub token (HTTPS git and API)", EnvVar: "GH_TOKEN",
		Rule:        Rule{Hosts: []string{"github.com", "api.github.com"}, Header: "Authorization", Template: `Basic {{base64 (print "x-access-token:" .Value)}}`},
		Description: "Personal access or app token for git over HTTPS and the GitHub API.",
	},
	{
		ID: "gitlab", Name: "GitLab token", EnvVar: "GITLAB_TOKEN",
		Rule:        Rule{Hosts: []string{"gitlab.com"}, Header: "Authorization", Template: "Bearer {{.Value}}"},
		Description: "GitLab personal access token for gitlab.com.",
	},
	{
		ID: "openrouter", Name: "OpenRouter API key", EnvVar: "OPENROUTER_API_KEY",
		Rule:        Rule{Hosts: []string{"openrouter.ai"}, Header: "Authorization", Template: "Bearer {{.Value}}"},
		Description: "OpenRouter API key.",
	},
}
