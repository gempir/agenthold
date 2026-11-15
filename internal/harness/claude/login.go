package claude

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/harness"
)

// Account logins for Claude Code.
//
// A subscription login is the long-lived OAuth token from `claude setup-token`
// (valid for a year, sent as a bearer token to api.anthropic.com). Claude Code
// reads it from CLAUDE_CODE_OAUTH_TOKEN and never refreshes it, so many
// sandboxes can share it. The regular `claude /login` credentials
// (~/.claude/.credentials.json) are short-lived with a rotating refresh token
// and are not accepted here.
//
// Both kinds reach the harness as placeholders; the egress proxy replaces
// them in the one header Claude Code sends them in.

const apiHost = "api.anthropic.com"

// Login kinds.
const (
	KindOAuthToken = "oauth-token"
	KindAPIKey     = "api-key"
)

// setupTokenLifetime is how long `claude setup-token` says its tokens last.
const setupTokenLifetime = 365 * 24 * time.Hour

// LoginKinds implements harness.AccountLoginer.
func (adapter) LoginKinds() []harness.LoginKind {
	return []harness.LoginKind{
		{
			ID: KindOAuthToken, Name: "Claude subscription",
			Description: "A long-lived token for your Claude Pro or Max plan. Run the command on any computer with a browser, sign in, and paste the token it prints.",
			Command:     "claude setup-token", InputLabel: "Token (sk-ant-oat…)",
		},
		{
			ID: KindAPIKey, Name: "Anthropic API key",
			Description: "An API key from the Claude Console, billed per use.",
			InputLabel:  "API key (sk-ant-…)",
		},
	}
}

// ParseLogin implements harness.AccountLoginer.
func (adapter) ParseLogin(_ context.Context, _ *http.Client, kind, input string) (harness.Credential, error) {
	v := strings.TrimSpace(input)
	if strings.ContainsAny(v, " \t\r\n") || len(v) > 4096 {
		return harness.Credential{}, errors.New("paste only the token, on one line")
	}
	switch kind {
	case KindOAuthToken:
		if !strings.HasPrefix(v, "sk-ant-oat") {
			return harness.Credential{}, errors.New("this is not a token from `claude setup-token` (they start with sk-ant-oat)")
		}
		return harness.Credential{Kind: kind, Secret: []byte(v), Label: "Token " + harness.Hint(v), ExpiresAt: time.Now().Add(setupTokenLifetime)}, nil
	case KindAPIKey:
		if !strings.HasPrefix(v, "sk-ant-") || strings.HasPrefix(v, "sk-ant-oat") {
			return harness.Credential{}, errors.New("this is not an Anthropic API key (they start with sk-ant-)")
		}
		return harness.Credential{Kind: kind, Secret: []byte(v), Label: "API key " + harness.Hint(v)}, nil
	}
	return harness.Credential{}, errors.New("unknown login kind")
}

// Deliver implements harness.AccountLoginer.
func (adapter) Deliver(c harness.Credential, seed string) (harness.Delivery, error) {
	switch c.Kind {
	case KindOAuthToken:
		ph := harness.Placeholder("sk-ant-oat01-", seed)
		return harness.Delivery{
			Env:        map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": ph},
			Injections: []harness.Injection{{Hosts: []string{apiHost}, Header: "Authorization", Placeholder: ph, Value: string(c.Secret)}},
		}, nil
	case KindAPIKey:
		ph := harness.Placeholder("sk-ant-api03-", seed)
		return harness.Delivery{
			Env:        map[string]string{"ANTHROPIC_API_KEY": ph},
			Injections: []harness.Injection{{Hosts: []string{apiHost}, Header: "X-Api-Key", Placeholder: ph, Value: string(c.Secret)}},
		}, nil
	}
	return harness.Delivery{}, errors.New("unknown login kind")
}

// ManagedFiles implements harness.AccountLoginer: Claude Code logins travel
// in the environment only.
func (adapter) ManagedFiles() []string { return nil }
