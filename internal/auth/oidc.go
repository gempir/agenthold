package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/gempir/agenthold/internal/config"
)

type oidcProvider struct {
	cfg         config.Provider
	redirectURL string
	role        *roleExpr

	mu       sync.Mutex
	provider *oidc.Provider // discovered lazily so the server starts while the IdP is down
}

func newOIDC(p config.Provider, redirectURL string) (*oidcProvider, error) {
	role, err := compileRoleExpr(p.RoleAttributePath)
	if err != nil {
		return nil, err
	}
	return &oidcProvider{cfg: p, redirectURL: redirectURL, role: role}, nil
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
	out := &Claims{
		Subject:       idToken.Subject,
		Email:         c.Email,
		EmailVerified: c.EmailVerified != nil && *c.EmailVerified,
		Name:          c.Name,
		Username:      c.PreferredUsername,
		AvatarURL:     c.Picture,
	}
	if p.role != nil {
		role, err := p.evalRole(ctx, provider, conf, tok, idToken)
		if err != nil {
			return nil, err
		}
		out.ProviderRole = role
	}
	return out, nil
}

// evalRole runs role_attribute_path over the ID token's claims merged with
// the userinfo response's (the ID token wins a conflict), like Grafana:
// Keycloak puts client roles in one or the other, depending on its mappers.
// Any failure refuses the sign-in rather than falling back to a default.
func (p *oidcProvider) evalRole(ctx context.Context, provider *oidc.Provider, conf *oauth2.Config, tok *oauth2.Token, idToken *oidc.IDToken) (config.Role, error) {
	var raw json.RawMessage
	if err := idToken.Claims(&raw); err != nil {
		return "", fmt.Errorf("decode claims: %w", err)
	}
	claims := claimsMap(raw)
	var userinfo map[string]any
	if provider.UserInfoEndpoint() != "" {
		info, err := provider.UserInfo(ctx, conf.TokenSource(ctx, tok))
		if err != nil {
			return "", fmt.Errorf("userinfo for role_attribute_path: %w", err)
		}
		if info.Subject != idToken.Subject {
			return "", errors.New("userinfo subject does not match the id_token")
		}
		if err := info.Claims(&userinfo); err != nil {
			return "", fmt.Errorf("decode userinfo: %w", err)
		}
	}
	role, err := p.role.eval(mergeClaims(claims, userinfo))
	if err != nil {
		slog.WarnContext(ctx, "role_attribute_path", "provider", p.cfg.ID, "err", err)
	}
	return role, err
}
