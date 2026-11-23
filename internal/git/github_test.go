package git_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/git"
)

// fakeGitHub is GitHub's device flow, token refresh, /user and token
// revocation endpoints.
type fakeGitHub struct {
	t   *testing.T
	srv *httptest.Server

	mu        sync.Mutex
	approved  bool
	denied    bool
	slowDown  bool
	expiring  bool // GitHub App: tokens expire and come with refresh tokens
	issued    int
	refreshed []string // refresh tokens presented
	revoked   []string
	scope     string
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	g := &fakeGitHub{t: t}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "Iv1.client" {
			http.Error(w, "bad client", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.scope = r.Form.Get("scope")
		g.mu.Unlock()
		writeJSON(w, map[string]any{
			"device_code": "dev-secret", "user_code": "ABCD-1234",
			"verification_uri": g.srv.URL + "/login/device", "expires_in": 900, "interval": 5,
		})
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		g.mu.Lock()
		defer g.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if r.Form.Get("device_code") != "dev-secret" {
				writeJSON(w, map[string]any{"error": "incorrect_device_code"})
				return
			}
			switch {
			case g.denied:
				writeJSON(w, map[string]any{"error": "access_denied"})
			case g.slowDown:
				g.slowDown = false
				writeJSON(w, map[string]any{"error": "slow_down", "interval": 10})
			case !g.approved:
				writeJSON(w, map[string]any{"error": "authorization_pending"})
			default:
				writeJSON(w, g.token())
			}
		case "refresh_token":
			if r.Form.Get("client_secret") != "shh" {
				writeJSON(w, map[string]any{"error": "incorrect_client_credentials"})
				return
			}
			g.refreshed = append(g.refreshed, r.Form.Get("refresh_token"))
			writeJSON(w, g.token())
		default:
			writeJSON(w, map[string]any{"error": "unsupported_grant_type"})
		}
	})
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer gho_") {
			http.Error(w, "Bad credentials", http.StatusUnauthorized)
			return
		}
		writeJSON(w, map[string]any{"login": "octo-ada", "id": 42})
	})
	mux.HandleFunc("DELETE /applications/Iv1.client/token", func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "Iv1.client" || p != "shh" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		var body struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		g.mu.Lock()
		g.revoked = append(g.revoked, body.AccessToken)
		g.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	g.srv = httptest.NewServer(mux)
	t.Cleanup(g.srv.Close)
	return g
}

// token issues the next access token (callers hold mu).
func (g *fakeGitHub) token() map[string]any {
	g.issued++
	tok := map[string]any{"access_token": "gho_" + string(rune('a'-1+g.issued)), "token_type": "bearer", "scope": "repo,read:org"}
	if g.expiring {
		tok["scope"] = ""
		tok["expires_in"] = 28800
		tok["refresh_token"] = "ghr_" + string(rune('a'-1+g.issued))
		tok["refresh_token_expires_in"] = 15897600
	}
	return tok
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// clock is a settable time for the service.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func withGitHub(t *testing.T, f *fixture, gh *fakeGitHub, secret string) *clock {
	t.Helper()
	cfg := &config.Config{Git: config.Git{GitHub: config.GitHubConnect{ClientID: "Iv1.client", ClientSecret: secret, URL: gh.srv.URL}}}
	config.ApplyDefaults(cfg)
	cfg.Git.GitHub.APIURL = gh.srv.URL
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	c := &clock{now: time.Unix(1000, 0)}
	f.svc.Now = c.Now
	f.svc.GitHub = &git.Connector{Config: cfg.Git.GitHub, HTTP: gh.srv.Client()}
	return c
}

func TestGitHubDeviceFlow(t *testing.T) {
	f := setup(t)
	gh := newFakeGitHub(t)
	c := withGitHub(t, f, gh, "shh")
	ctx := t.Context()

	flow, err := f.svc.StartGitHub(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	if flow.UserCode != "ABCD-1234" || flow.VerificationURI != gh.srv.URL+"/login/device" || flow.Interval != 5 {
		t.Fatalf("flow = %+v", flow)
	}
	if b, _ := json.Marshal(flow); strings.Contains(string(b), "dev-secret") {
		t.Fatalf("the device code left the server: %s", b)
	}
	if gh.scope != "repo read:org workflow gist" {
		t.Fatalf("scope = %q", gh.scope)
	}
	// Another user cannot poll (or cancel) the flow.
	if _, err := f.svc.PollGitHub(ctx, "usr_2", flow.ID); !errors.Is(err, git.ErrFlowNotFound) {
		t.Fatalf("other user's poll: %v", err)
	}
	f.svc.CancelGitHub("usr_2", flow.ID)

	// Before the interval nothing is asked; then GitHub says pending, then slow down.
	poll := func(want string) git.PollResult {
		t.Helper()
		r, err := f.svc.PollGitHub(ctx, "usr_1", flow.ID)
		if err != nil || r.Status != want {
			t.Fatalf("poll = %+v, %v; want %s", r, err, want)
		}
		return r
	}
	poll(git.FlowPending)
	c.Add(5 * time.Second)
	poll(git.FlowPending)
	gh.mu.Lock()
	gh.slowDown = true
	gh.mu.Unlock()
	c.Add(5 * time.Second)
	poll(git.FlowPending) // slow_down: the interval grows to 10 s
	gh.mu.Lock()
	gh.approved = true
	gh.mu.Unlock()
	c.Add(5 * time.Second)
	poll(git.FlowPending) // still within the longer interval: GitHub is not asked
	if gh.issued != 0 {
		t.Fatal("polled GitHub before the interval")
	}
	c.Add(5 * time.Second)
	r := poll(git.FlowConnected)
	cr := r.Credential
	if cr == nil || cr.Host != "127.0.0.1" || cr.Source != git.SourceGitHub || cr.Account != "octo-ada" ||
		cr.Label != "@octo-ada" || cr.Username != "x-access-token" || cr.Scopes != "repo,read:org" || cr.ExpiresAt != 0 {
		t.Fatalf("credential = %+v", cr)
	}
	// The flow is gone once used.
	if _, err := f.svc.PollGitHub(ctx, "usr_1", flow.ID); !errors.Is(err, git.ErrFlowNotFound) {
		t.Fatalf("poll after connecting: %v", err)
	}

	// The proxy injects the token for the owner's agents.
	injs, err := f.svc.Egress(ctx, f.agent)
	if err != nil || len(injs) != 1 {
		t.Fatalf("egress = %+v, %v", injs, err)
	}
	if want := base64.StdEncoding.EncodeToString([]byte("x-access-token:gho_a")); injs[0].Value != want {
		t.Fatalf("injected %q", injs[0].Value)
	}
	settings, err := f.svc.GetSettings(ctx, f.user)
	if err != nil || len(settings.Credentials) != 1 || settings.Credentials[0].Account != "octo-ada" {
		t.Fatalf("settings = %+v, %v", settings, err)
	}

	// Reconnecting replaces the token and revokes the old one at GitHub.
	flow2, err := f.svc.StartGitHub(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(5 * time.Second)
	r2, err := f.svc.PollGitHub(ctx, "usr_1", flow2.ID)
	if err != nil || r2.Credential == nil || r2.Credential.ID != cr.ID {
		t.Fatalf("reconnect = %+v, %v", r2, err)
	}
	if len(gh.revoked) != 1 || gh.revoked[0] != "gho_a" {
		t.Fatalf("revoked after reconnect = %v", gh.revoked)
	}

	// Pasted tokens stay plain tokens next to it.
	tc, err := f.svc.UpsertCredential(ctx, "usr_1", "gitlab.com", "", "glpat", "")
	if err != nil || tc.Source != git.SourceToken {
		t.Fatalf("paste = %+v, %v", tc, err)
	}

	// Disconnecting revokes the token at GitHub.
	if err := f.svc.DeleteCredential(ctx, "usr_1", cr.ID); err != nil {
		t.Fatal(err)
	}
	if len(gh.revoked) != 2 || gh.revoked[1] != "gho_b" {
		t.Fatalf("revoked = %v", gh.revoked)
	}
}

func TestGitHubDeviceFlowDenied(t *testing.T) {
	f := setup(t)
	gh := newFakeGitHub(t)
	c := withGitHub(t, f, gh, "")
	flow, err := f.svc.StartGitHub(t.Context(), "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	gh.denied = true
	c.Add(5 * time.Second)
	if r, err := f.svc.PollGitHub(t.Context(), "usr_1", flow.ID); err != nil || r.Status != git.FlowDenied {
		t.Fatalf("poll = %+v, %v", r, err)
	}
	// A new flow replaces an older one of the same user; flows expire.
	a, _ := f.svc.StartGitHub(t.Context(), "usr_1")
	b, _ := f.svc.StartGitHub(t.Context(), "usr_1")
	if _, err := f.svc.PollGitHub(t.Context(), "usr_1", a.ID); !errors.Is(err, git.ErrFlowNotFound) {
		t.Fatalf("replaced flow: %v", err)
	}
	c.Add(16 * time.Minute)
	if _, err := f.svc.PollGitHub(t.Context(), "usr_1", b.ID); !errors.Is(err, git.ErrFlowNotFound) {
		t.Fatalf("expired flow: %v", err)
	}
}

func TestGitHubDisabled(t *testing.T) {
	f := setup(t)
	if _, err := f.svc.StartGitHub(t.Context(), "usr_1"); !errors.Is(err, git.ErrGitHubDisabled) {
		t.Fatalf("start without config: %v", err)
	}
}

func TestGitHubAppTokenRefresh(t *testing.T) {
	f := setup(t)
	gh := newFakeGitHub(t)
	gh.expiring, gh.approved = true, true
	c := withGitHub(t, f, gh, "shh")
	ctx := t.Context()
	flow, err := f.svc.StartGitHub(ctx, "usr_1")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(5 * time.Second)
	r, err := f.svc.PollGitHub(ctx, "usr_1", flow.ID)
	if err != nil || r.Credential == nil || r.Credential.ExpiresAt != c.Now().Unix()+28800 {
		t.Fatalf("poll = %+v, %v", r, err)
	}

	// Not due yet: nothing happens.
	f.svc.RefreshDue(ctx)
	if len(gh.refreshed) != 0 {
		t.Fatalf("refreshed early: %v", gh.refreshed)
	}
	// Within the refresh window it is renewed with the stored refresh token.
	c.Add(28800*time.Second - 10*time.Minute)
	before := f.invalidated
	f.svc.RefreshDue(ctx)
	if len(gh.refreshed) != 1 || gh.refreshed[0] != "ghr_a" || f.invalidated == before {
		t.Fatalf("refreshed = %v (invalidated %d→%d)", gh.refreshed, before, f.invalidated)
	}
	injs, err := f.svc.Egress(ctx, f.agent)
	if err != nil || len(injs) != 1 || injs[0].Value != base64.StdEncoding.EncodeToString([]byte("x-access-token:gho_b")) {
		t.Fatalf("egress after refresh = %+v, %v", injs, err)
	}
	// The rotated refresh token is used next time.
	c.Add(28800*time.Second - 5*time.Minute)
	f.svc.RefreshDue(ctx)
	if len(gh.refreshed) != 2 || gh.refreshed[1] != "ghr_b" {
		t.Fatalf("second refresh = %v", gh.refreshed)
	}
}

func TestGitHubConfigValidation(t *testing.T) {
	for name, gh := range map[string]config.GitHubConnect{
		"http":        {ClientID: "x", URL: "http://github.example.com"},
		"credentials": {ClientID: "x", URL: "https://u:p@github.example.com"},
		"client id":   {ClientID: "a b"},
	} {
		cfg := &config.Config{Git: config.Git{GitHub: gh}}
		config.ApplyDefaults(cfg)
		if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "git.github") {
			t.Errorf("%s: %v", name, err)
		}
	}
	cfg := &config.Config{Git: config.Git{GitHub: config.GitHubConnect{ClientID: "x", URL: "https://ghe.example.com/"}}}
	config.ApplyDefaults(cfg)
	if g := cfg.Git.GitHub; g.APIURL != "https://ghe.example.com/api/v3" || g.Host() != "ghe.example.com" {
		t.Fatalf("enterprise defaults = %+v", g)
	}
	cfg = &config.Config{Git: config.Git{GitHub: config.GitHubConnect{ClientID: "x"}}}
	config.ApplyDefaults(cfg)
	if g := cfg.Git.GitHub; g.URL != "https://github.com" || g.APIURL != "https://api.github.com" || g.Host() != "github.com" {
		t.Fatalf("defaults = %+v", g)
	}
}
