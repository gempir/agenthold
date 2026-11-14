package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/gempir/agenthold/internal/config"
)

type oidcProvider struct {
	cfg         config.Provider
	redirectURL string

	mu       sync.Mutex
	provider *oidc.Provider // discovered lazily so the server starts while the IdP is down
}

func newOIDC(p config.Provider, redirectURL string) *oidcProvider {
	return &oidcProvider{cfg: p, redirectURL: redirectURL}
}

func (p *oidcProvider) Info() config.Provider { return p.cfg }

func (p *oidcProvider) discover(ctx context.Context) (*oidc.Provider, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.provider != nil {
		return p.provider, nil
	}
	discoveryURL := p.cfg.Issuer
	if p.cfg.DiscoveryURL != "" {
		// The IdP is reachable under a different name from here than from the
		// browser. Tokens must still carry the public issuer.
		discoveryURL = p.cfg.DiscoveryURL
		ctx = oidc.InsecureIssuerURLContext(ctx, p.cfg.Issuer)
	}
	provider, err := oidc.NewProvider(ctx, discoveryURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", p.cfg.ID, err)
	}
	p.provider = provider
	return provider, nil
}

func (p *oidcProvider) oauth2(provider *oidc.Provider) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     p.cfg.ClientID,
		ClientSecret: p.cfg.ClientSecret,
		RedirectURL:  p.redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       p.cfg.Scopes,
	}
}

func (p *oidcProvider) AuthCodeURL(ctx context.Context, state, verifier, nonce string) (string, error) {
	provider, err := p.discover(ctx)
	if err != nil {
		return "", err
	}
	return p.oauth2(provider).AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oidc.Nonce(nonce)), nil
}

func (p *oidcProvider) Exchange(ctx context.Context, code, verifier, nonce string) (*Claims, error) {
	provider, err := p.discover(ctx)
	if err != nil {
		return nil, err
	}
	conf := p.oauth2(provider)
	tok, err := conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok {
		return nil, errors.New("token response has no id_token")
	}
	idToken, err := provider.VerifierContext(ctx, &oidc.Config{ClientID: p.cfg.ClientID}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("verify id_token: %w", err)
	}
	if idToken.Nonce != nonce {
		return nil, errors.New("id_token nonce mismatch")
	}
	var c struct {
		Email             string `json:"email"`
		EmailVerified     *bool  `json:"email_verified"`
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Picture           string `json:"picture"`
	}
	if err := idToken.Claims(&c); err != nil {
		return nil, fmt.Errorf("decode claims: %w", err)
	}
	return &Claims{
		Subject:       idToken.Subject,
		Email:         c.Email,
		EmailVerified: c.EmailVerified != nil && *c.EmailVerified,
		Name:          c.Name,
		Username:      c.PreferredUsername,
		AvatarURL:     c.Picture,
	}, nil
}
