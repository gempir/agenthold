package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/harness"
)

// Account logins for Codex (behaviour of the Codex CLI 0.166 that
// codex-acp 2.6.0 runs, read from its source):
//
//   - ChatGPT login: `codex login` writes $CODEX_HOME/auth.json with an
//     id_token, a JWT access token and a refresh token. Codex refreshes the
//     access token shortly before it expires (or 8 days after the last
//     refresh when it cannot read the expiry, or after a 401) by POSTing the
//     refresh token to auth.openai.com/oauth/token. Refresh tokens rotate:
//     the first sandbox to refresh would log out every other one. So the
//     server owns the tokens and refreshes them; each sandbox gets an
//     auth.json with placeholders, the proxy puts the current access token
//     into requests to chatgpt.com, and the platform answers the harness's
//     own refresh requests itself.
//   - ChatGPT access token ("at-…", CODEX_ACCESS_TOKEN): long-lived, no refresh.
//   - API key (OPENAI_API_KEY).

// TokenURL is where ChatGPT tokens are refreshed (tests point it elsewhere).
var TokenURL = "https://auth.openai.com/oauth/token"

// ClientID is the Codex CLI's OAuth client: refreshed tokens must come from
// the client that issued them.
const ClientID = "app_EMoamEEZ73f0CkXaXp7hrann"

const authFile = ".codex/auth.json"

// Login kinds.
const (
	KindChatGPT     = "chatgpt"
	KindAccessToken = "access-token"
	KindAPIKey      = "api-key"
)

// LoginKinds implements harness.AccountLoginer.
func (logins) LoginKinds() []harness.LoginKind {
	return []harness.LoginKind{
		{
			ID: KindChatGPT, Name: "ChatGPT login",
			Description: "Your ChatGPT plan. Sign in with a temporary Codex home so the login on your computer is not affected, then paste the auth.json it wrote. agenthold takes the login over and refreshes it.",
			Command:     `export CODEX_HOME="$(mktemp -d)"; codex login --device-auth && cat "$CODEX_HOME/auth.json"; rm -rf "$CODEX_HOME"`,
			InputLabel:  "Contents of auth.json", Multiline: true,
		},
		{
			ID: KindAccessToken, Name: "ChatGPT access token",
			Description: "A long-lived ChatGPT access token for Codex, if your ChatGPT workspace issues them.",
			InputLabel:  "Access token (at-…)",
		},
		{
			ID: KindAPIKey, Name: "OpenAI API key",
			Description: "An API key from the OpenAI platform, billed per use.",
			InputLabel:  "API key (sk-…)",
		},
	}
}

// tokens is what the server stores for a ChatGPT login.
type tokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id,omitempty"`
}

// ParseLogin implements harness.AccountLoginer.
func (a logins) ParseLogin(ctx context.Context, client *http.Client, kind, input string) (harness.Credential, error) {
	v := strings.TrimSpace(input)
	switch kind {
	case KindAPIKey, KindAccessToken:
		if strings.ContainsAny(v, " \t\r\n") || len(v) > 4096 || v == "" {
			return harness.Credential{}, errors.New("paste only the key, on one line")
		}
		if kind == KindAccessToken {
			if !strings.HasPrefix(v, "at-") {
				return harness.Credential{}, errors.New("ChatGPT access tokens start with at-")
			}
			return harness.Credential{Kind: kind, Secret: []byte(v), Label: "Access token " + harness.Hint(v)}, nil
		}
		if !strings.HasPrefix(v, "sk-") {
			return harness.Credential{}, errors.New("OpenAI API keys start with sk-")
		}
		return harness.Credential{Kind: kind, Secret: []byte(v), Label: "API key " + harness.Hint(v)}, nil
	case KindChatGPT:
		var f struct {
			Tokens *tokens `json:"tokens"`
		}
		if len(v) > 64*1024 || json.Unmarshal([]byte(v), &f) != nil {
			return harness.Credential{}, errors.New("paste the whole auth.json file")
		}
		t := f.Tokens
		if t == nil || t.IDToken == "" || t.AccessToken == "" || t.RefreshToken == "" {
			return harness.Credential{}, errors.New("this auth.json has no ChatGPT login (sign in with `codex login`, not an API key)")
		}
		if _, err := jwtClaims(t.IDToken); err != nil {
			return harness.Credential{}, errors.New("the id_token in auth.json is not readable")
		}
		c, err := credentialFor(*t)
		if err != nil {
			return c, err
		}
		// Take the login over: from now on only the server refreshes it, and
		// this checks that the refresh token is still good.
		return a.Refresh(ctx, client, c)
	}
	return harness.Credential{}, errors.New("unknown login kind")
}

func credentialFor(t tokens) (harness.Credential, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return harness.Credential{}, err
	}
	c := harness.Credential{Kind: KindChatGPT, Secret: b, Label: "ChatGPT"}
	if claims, err := jwtClaims(t.IDToken); err == nil {
		label := []string{"ChatGPT"}
		if email := idEmail(claims); email != "" {
			label = append(label, email)
		}
		if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
			if plan, ok := auth["chatgpt_plan_type"].(string); ok && plan != "" {
				label = append(label, "("+plan+")")
			}
		}
		c.Label = strings.Join(label, " ")
	}
	if claims, err := jwtClaims(t.AccessToken); err == nil {
		if exp, ok := claims["exp"].(float64); ok && exp > 0 {
			c.ExpiresAt = time.Unix(int64(exp), 0)
		}
	}
	if c.ExpiresAt.IsZero() {
		// Codex's own rule when it cannot read the expiry.
		c.ExpiresAt = time.Now().Add(7 * 24 * time.Hour)
	}
	return c, nil
}

func idEmail(claims map[string]any) string {
	if e, ok := claims["email"].(string); ok {
		return e
	}
	if p, ok := claims["https://api.openai.com/profile"].(map[string]any); ok {
		if e, ok := p["email"].(string); ok {
			return e
		}
	}
	return ""
}

func jwtClaims(jwt string) (map[string]any, error) {
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWT")
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func unsignedJWT(claims map[string]any, signature string) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none", "typ": "JWT"}) + "." + enc(claims) + "." + signature
}

// placeholderIDToken carries the claims Codex reads (e-mail, plan, account)
// without the provider's signature, so no token issued by OpenAI enters the
// sandbox.
func placeholderIDToken(real string) string {
	claims, _ := jwtClaims(real)
	out := map[string]any{}
	for _, k := range []string{"email", "https://api.openai.com/profile", "https://api.openai.com/auth"} {
		if v, ok := claims[k]; ok {
			out[k] = v
		}
	}
	return unsignedJWT(out, "agenthold")
}

// placeholderAccessToken looks like a JWT that never expires, so Codex does
// not try to refresh it on its own; the proxy replaces all of it.
func placeholderAccessToken(seed string) string {
	return unsignedJWT(map[string]any{"exp": 4102444800, "iss": "agenthold"}, harness.PlaceholderMarker+seed)
}

func placeholderRefreshToken(seed string) string {
	return harness.Placeholder("", seed+"-refresh")
}

func accountID(t tokens) string {
	if t.AccountID != "" {
		return t.AccountID
	}
	claims, _ := jwtClaims(t.IDToken)
	if auth, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if id, ok := auth["chatgpt_account_id"].(string); ok {
			return id
		}
	}
	return ""
}

// Deliver implements harness.AccountLoginer.
func (logins) Deliver(c harness.Credential, seed string) (harness.Delivery, error) {
	switch c.Kind {
	case KindAPIKey:
		ph := harness.Placeholder("sk-", seed)
		file, _ := json.Marshal(map[string]any{"OPENAI_API_KEY": ph})
		return harness.Delivery{
			Env:        map[string]string{"OPENAI_API_KEY": ph},
			Files:      map[string][]byte{authFile: file},
			Injections: []harness.Injection{{Hosts: []string{"api.openai.com"}, Header: "Authorization", Placeholder: ph, Value: string(c.Secret)}},
		}, nil
	case KindAccessToken:
		ph := harness.Placeholder("at-", seed)
		return harness.Delivery{
			Env: map[string]string{"CODEX_ACCESS_TOKEN": ph},
			Injections: []harness.Injection{
				{Hosts: []string{"auth.openai.com"}, Header: "Authorization", PathPrefixes: []string{"/api/accounts/v1/user-auth-credential/whoami"}, Placeholder: ph, Value: string(c.Secret)},
				{Hosts: []string{"chatgpt.com"}, Header: "Authorization", PathPrefixes: []string{"/backend-api/"}, Placeholder: ph, Value: string(c.Secret)},
			},
		}, nil
	case KindChatGPT:
		var t tokens
		if err := json.Unmarshal(c.Secret, &t); err != nil {
			return harness.Delivery{}, err
		}
		access, refresh := placeholderAccessToken(seed), placeholderRefreshToken(seed)
		file, err := json.MarshalIndent(map[string]any{
			"OPENAI_API_KEY": nil,
			"tokens": map[string]any{
				"id_token": placeholderIDToken(t.IDToken), "access_token": access, "refresh_token": refresh,
				"account_id": accountID(t),
			},
			"last_refresh": time.Now().UTC().Format(time.RFC3339),
		}, "", "  ")
		if err != nil {
			return harness.Delivery{}, err
		}
		return harness.Delivery{
			Files:      map[string][]byte{authFile: file},
			Injections: []harness.Injection{{Hosts: []string{"chatgpt.com"}, Header: "Authorization", PathPrefixes: []string{"/backend-api/"}, Placeholder: access, Value: t.AccessToken}},
			Intercepts: []harness.Intercept{{Host: "auth.openai.com", Method: http.MethodPost, Path: "/oauth/token", Placeholder: refresh}},
		}, nil
	}
	return harness.Delivery{}, errors.New("unknown login kind")
}

// ManagedFiles implements harness.AccountLoginer.
func (logins) ManagedFiles() []string { return []string{authFile} }

// RefreshDue implements harness.Refresher: an hour before the access token expires.
func (logins) RefreshDue(c harness.Credential, now time.Time) bool {
	return c.Kind == KindChatGPT && !c.ExpiresAt.IsZero() && now.Add(time.Hour).After(c.ExpiresAt)
}

// Refresh implements harness.Refresher.
func (logins) Refresh(ctx context.Context, client *http.Client, c harness.Credential) (harness.Credential, error) {
	if c.Kind != KindChatGPT {
		return c, nil
	}
	var t tokens
	if err := json.Unmarshal(c.Secret, &t); err != nil {
		return c, err
	}
	body, _ := json.Marshal(map[string]string{"client_id": ClientID, "grant_type": "refresh_token", "refresh_token": t.RefreshToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, bytes.NewReader(body))
	if err != nil {
		return c, err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return c, fmt.Errorf("refresh ChatGPT login: %w", err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode == http.StatusBadRequest || res.StatusCode == http.StatusUnauthorized {
		// Never include the body: the provider may echo the token.
		return c, fmt.Errorf("refresh ChatGPT login: status %d: %w", res.StatusCode, harness.ErrLoginRevoked)
	}
	if res.StatusCode != http.StatusOK {
		return c, fmt.Errorf("refresh ChatGPT login: status %d", res.StatusCode)
	}
	var r struct {
		IDToken      string `json:"id_token"`
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.AccessToken == "" {
		return c, errors.New("refresh ChatGPT login: unexpected response")
	}
	t.AccessToken = r.AccessToken
	if r.IDToken != "" {
		t.IDToken = r.IDToken
	}
	if r.RefreshToken != "" {
		t.RefreshToken = r.RefreshToken
	}
	return credentialFor(t)
}

// InterceptResponse implements harness.Refresher: what Codex receives when it
// refreshes on its own. The placeholders stay the same; the proxy injects the
// access token the server just refreshed.
func (logins) InterceptResponse(c harness.Credential, seed string) ([]byte, error) {
	var t tokens
	if err := json.Unmarshal(c.Secret, &t); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]string{
		"id_token": placeholderIDToken(t.IDToken), "access_token": placeholderAccessToken(seed), "refresh_token": placeholderRefreshToken(seed),
	})
}

// logins implements harness.AccountLoginer and, where credentials expire, harness.Refresher.
type logins struct{}
