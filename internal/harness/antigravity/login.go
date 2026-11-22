package antigravity

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/gempir/agenthold/internal/harness"
)

// Account logins for Antigravity: a Gemini API key only.
//
// The key reaches agy as a placeholder in GEMINI_API_KEY; the egress proxy
// puts the real key into the x-goog-api-key header on requests to the Gemini
// API, the only header agy sends it in.
//
// Google sign-ins are not offered: agy keeps them in its own token file
// (with a refresh token) whose format is not documented, so the server could
// not take one over and refresh it for many sandboxes. They work inside a
// sandbox instead (terminal-login), where the agent can read them.

// GeminiAPIHost is where agy sends Gemini API requests.
const GeminiAPIHost = "generativelanguage.googleapis.com"

// KindAPIKey is the login kind of a Gemini API key.
const KindAPIKey = "api-key"

// LoginKinds implements harness.AccountLoginer.
func (logins) LoginKinds() []harness.LoginKind {
	return []harness.LoginKind{{
		ID: KindAPIKey, Name: "Gemini API key",
		Description: "An API key from Google AI Studio (Gemini Developer API), billed to its project.",
		InputLabel:  "API key (AIza…)",
	}}
}

// ParseLogin implements harness.AccountLoginer.
func (logins) ParseLogin(_ context.Context, _ *http.Client, kind, input string) (harness.Credential, error) {
	if kind != KindAPIKey {
		return harness.Credential{}, errors.New("unknown login kind")
	}
	v := strings.TrimSpace(input)
	if strings.ContainsAny(v, " \t\r\n") || len(v) > 4096 {
		return harness.Credential{}, errors.New("paste only the key, on one line")
	}
	if !strings.HasPrefix(v, "AIza") || len(v) < 30 {
		return harness.Credential{}, errors.New("this is not a Google API key (they start with AIza)")
	}
	return harness.Credential{Kind: kind, Secret: []byte(v), Label: "API key " + harness.Hint(v)}, nil
}

// Deliver implements harness.AccountLoginer.
func (logins) Deliver(c harness.Credential, seed string) (harness.Delivery, error) {
	if c.Kind != KindAPIKey {
		return harness.Delivery{}, errors.New("unknown login kind")
	}
	ph := harness.Placeholder("AIza", seed)
	return harness.Delivery{
		Env:        map[string]string{"GEMINI_API_KEY": ph},
		Injections: []harness.Injection{{Hosts: []string{GeminiAPIHost}, Header: "X-Goog-Api-Key", Placeholder: ph, Value: string(c.Secret)}},
	}, nil
}

// ManagedFiles implements harness.AccountLoginer: the key travels in the
// environment only.
func (logins) ManagedFiles() []string { return nil }

// logins implements harness.AccountLoginer.
type logins struct{}
