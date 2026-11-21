package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"golang.org/x/oauth2"

	"github.com/gempir/agenthold/internal/config"
)

// githubProvider implements GitHub's OAuth2 web flow. GitHub user login is not
// OIDC, so identity comes from the REST API instead of an ID token.
type githubProvider struct {
	cfg  config.Provider
	conf *oauth2.Config
}

func newGitHub(p config.Provider, redirectURL string) *githubProvider {
	base := strings.TrimRight(p.GitHubURL, "/")
	return &githubProvider{cfg: p, conf: &oauth2.Config{
		ClientID:     p.ClientID,
		ClientSecret: p.ClientSecret,
		RedirectURL:  redirectURL,
		Scopes:       p.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  base + "/login/oauth/authorize",
			TokenURL: base + "/login/oauth/access_token",
		},
	}}
}

func (p *githubProvider) Info() config.Provider { return p.cfg }

func (p *githubProvider) AuthCodeURL(_ context.Context, state, verifier, _ string) (string, error) {
	return p.conf.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier)), nil
}

func (p *githubProvider) Exchange(ctx context.Context, code, verifier, _ string) (*Claims, error) {
	tok, err := p.conf.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return nil, fmt.Errorf("exchange code: %w", err)
	}
	client := p.conf.Client(ctx, tok)
	api := strings.TrimRight(p.cfg.GitHubAPIURL, "/")

	var user struct {
		ID        int64  `json:"id"`
		Login     string `json:"login"`
		Name      string `json:"name"`
		Email     string `json:"email"`
		AvatarURL string `json:"avatar_url"`
	}
	if err := getJSON(ctx, client, api+"/user", &user); err != nil {
		return nil, err
	}
	if user.ID == 0 {
		return nil, fmt.Errorf("github: user has no id")
	}
	c := &Claims{
		Subject:   strconv.FormatInt(user.ID, 10), // logins can be renamed, ids cannot
		Username:  user.Login,
		Name:      user.Name,
		AvatarURL: user.AvatarURL,
	}
	if c.Name == "" {
		c.Name = user.Login
	}
	c.ProviderRole = githubRole(p.cfg, c.Subject)

	var emails []struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}
	if err := getJSON(ctx, client, api+"/user/emails", &emails); err == nil {
		for _, e := range emails {
			if e.Primary {
				c.Email, c.EmailVerified = e.Email, e.Verified
			}
		}
	}
	if c.Email == "" {
		c.Email = user.Email // public profile email, unverified as far as we know
	}
	return c, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("github: GET %s: %s", url, res.Status)
	}
	return json.NewDecoder(res.Body).Decode(v)
}
