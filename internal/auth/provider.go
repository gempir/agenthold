// Package auth implements sign-in (OIDC and GitHub OAuth), browser sessions
// and the request middleware that identifies the current user.
package auth

import (
	"context"
	"fmt"

	"github.com/gempir/agenthold/internal/config"
)

// Claims is what a provider tells us about the person who signed in.
type Claims struct {
	Subject       string
	Email         string
	EmailVerified bool
	Name          string
	Username      string
	AvatarURL     string
}

// Provider is one configured identity provider.
type Provider interface {
	Info() config.Provider
	// AuthCodeURL returns the URL to send the browser to.
	AuthCodeURL(ctx context.Context, state, verifier, nonce string) (string, error)
	// Exchange redeems the authorization code and returns verified claims.
	Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error)
}

// NewProvider builds the provider for a config entry. redirectURL is the
// callback URL registered with the provider.
func NewProvider(p config.Provider, redirectURL string) (Provider, error) {
	switch p.Type {
	case config.ProviderOIDC:
		return newOIDC(p, redirectURL), nil
	case config.ProviderGitHub:
		return newGitHub(p, redirectURL), nil
	}
	return nil, fmt.Errorf("unknown provider type %q", p.Type)
}
