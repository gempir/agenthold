package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/harness"
)

// Account logins of the scripted agent, for tests: "token" travels in the
// environment like Claude Code's, "refreshing" is a file with a rotating
// token like Codex's ChatGPT login.

// TokenURL is where "refreshing" logins are renewed (tests set it).
var TokenURL = ""

// Hosts the scripted agent's logins are injected into.
const (
	APIHost  = "api.fake.example"
	AuthHost = "auth.fake.example"
)

// LoginKinds implements harness.AccountLoginer.
func (adapter) LoginKinds() []harness.LoginKind {
	return []harness.LoginKind{
		{ID: "token", Name: "Fake token", Description: "Any value starting with fake-.", InputLabel: "Token"},
		{ID: "refreshing", Name: "Fake rotating login", Description: `JSON {"access":…,"refresh":…}.`, InputLabel: "Login", Multiline: true},
	}
}

type rotating struct {
	Access  string `json:"access"`
	Refresh string `json:"refresh"`
	Expires int64  `json:"expires,omitempty"`
}

// ParseLogin implements harness.AccountLoginer.
func (adapter) ParseLogin(_ context.Context, _ *http.Client, kind, input string) (harness.Credential, error) {
	v := strings.TrimSpace(input)
	switch kind {
	case "token":
		if !strings.HasPrefix(v, "fake-") {
			return harness.Credential{}, errors.New("fake tokens start with fake-")
		}
		return harness.Credential{Kind: kind, Secret: []byte(v), Label: "Token " + harness.Hint(v)}, nil
	case "refreshing":
		var r rotating
		if json.Unmarshal([]byte(v), &r) != nil || r.Access == "" || r.Refresh == "" {
			return harness.Credential{}, errors.New(`expected {"access":…,"refresh":…}`)
		}
		return harness.Credential{Kind: kind, Secret: []byte(v), Label: "Rotating", ExpiresAt: time.Unix(r.Expires, 0)}, nil
	}
	return harness.Credential{}, errors.New("unknown login kind")
}

// Deliver implements harness.AccountLoginer.
func (adapter) Deliver(c harness.Credential, seed string) (harness.Delivery, error) {
	switch c.Kind {
	case "token":
		ph := harness.Placeholder("fake-", seed)
		return harness.Delivery{
			Env:        map[string]string{"FAKEAGENT_TOKEN": ph},
			Injections: []harness.Injection{{Hosts: []string{APIHost}, Header: "Authorization", Placeholder: ph, Value: string(c.Secret)}},
		}, nil
	case "refreshing":
		var r rotating
		if err := json.Unmarshal(c.Secret, &r); err != nil {
			return harness.Delivery{}, err
		}
		access, refresh := harness.Placeholder("", seed+"-a"), harness.Placeholder("", seed+"-r")
		file, _ := json.Marshal(map[string]string{"access": access, "refresh": refresh})
		return harness.Delivery{
			Files:      map[string][]byte{".fakeagent/account.json": file},
			Injections: []harness.Injection{{Hosts: []string{APIHost}, Header: "Authorization", Placeholder: access, Value: r.Access}},
			Intercepts: []harness.Intercept{{Host: AuthHost, Method: http.MethodPost, Path: "/token", Placeholder: refresh}},
		}, nil
	}
	return harness.Delivery{}, errors.New("unknown login kind")
}

// ManagedFiles implements harness.AccountLoginer.
func (adapter) ManagedFiles() []string { return []string{".fakeagent/account.json"} }

// RefreshDue implements harness.Refresher.
func (adapter) RefreshDue(c harness.Credential, now time.Time) bool {
	return c.Kind == "refreshing" && c.ExpiresAt.Unix() > 0 && now.Add(time.Hour).After(c.ExpiresAt)
}

// Refresh implements harness.Refresher: POST refresh=<token> to TokenURL,
// which answers with a new rotating login as JSON.
func (adapter) Refresh(ctx context.Context, client *http.Client, c harness.Credential) (harness.Credential, error) {
	var r rotating
	if err := json.Unmarshal(c.Secret, &r); err != nil {
		return c, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, TokenURL, strings.NewReader(url.Values{"refresh": {r.Refresh}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := client.Do(req)
	if err != nil {
		return c, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return c, fmt.Errorf("refresh: %w", harness.ErrLoginRevoked)
	}
	var next rotating
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&next) != nil || next.Access == "" {
		return c, fmt.Errorf("refresh: status %d", res.StatusCode)
	}
	b, _ := json.Marshal(next)
	return harness.Credential{Kind: c.Kind, Secret: b, Label: c.Label, ExpiresAt: time.Unix(next.Expires, 0)}, nil
}

// InterceptResponse implements harness.Refresher.
func (adapter) InterceptResponse(_ harness.Credential, seed string) ([]byte, error) {
	return json.Marshal(map[string]string{"access": harness.Placeholder("", seed+"-a"), "refresh": harness.Placeholder("", seed+"-r")})
}
