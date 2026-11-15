package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/auth/authtest"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	_ "github.com/gempir/agenthold/internal/harness/claude"
	_ "github.com/gempir/agenthold/internal/harness/codex"
	fakeharness "github.com/gempir/agenthold/internal/harness/fake"
	_ "github.com/gempir/agenthold/internal/harness/opencode"
	"github.com/gempir/agenthold/internal/idle"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/password"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tools"
	"github.com/gempir/agenthold/internal/trigger"
	"github.com/gempir/agenthold/internal/tunnel"
)

type env struct {
	t         *testing.T
	srv       *httptest.Server
	idp       *authtest.IdP
	github    *authtest.GitHub
	driver    *fake.Driver
	sandboxes *sandbox.Manager
	hub       *tunnel.Hub
	directory *egress.Directory
	logins    *logins.Service
	store     *store.Store
	sessions  *session.Manager
	tracker   *idle.Tracker
	sleeper   *idle.Sleeper
	clock     *clock
	scheduler *trigger.Scheduler
}

type slogDiscard struct{}

func (slogDiscard) Info(string, ...any) {}
func (slogDiscard) Warn(string, ...any) {}

// clock is a manual clock for idle tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time          { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

// sandboxdBin runs the platform MCP server ("sandboxd mcp") for fake sandboxes.
var sandboxdBin string

func TestMain(m *testing.M) {
	// The scripted ACP agent runs as each fake sandbox's harness.
	dir, err := os.MkdirTemp("", "fakeagent")
	if err != nil {
		panic(err)
	}
	bin := filepath.Join(dir, "fakeagent")
	if out, err := exec.Command("go", "build", "-o", bin, "github.com/gempir/agenthold/internal/harness/fakeagent").CombinedOutput(); err != nil {
		panic(string(out))
	}
	fakeharness.Command = bin
	sandboxdBin = filepath.Join(dir, "sandboxd")
	if out, err := exec.Command("go", "build", "-o", sandboxdBin, "github.com/gempir/agenthold/cmd/sandboxd").CombinedOutput(); err != nil {
		panic(string(out))
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func newEnv(t *testing.T) *env { return newEnvWith(t, nil) }

// newEnvWith builds an environment; configure may change the configuration
// (auth settings) before the server is built.
func newEnvWith(t *testing.T, configure func(*config.Config)) *env {
	t.Helper()
	idp := authtest.NewIdP(t)
	gh := authtest.NewGitHub(t)

	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	cfg := &config.Config{PublicURL: srv.URL, Auth: config.Auth{Providers: []config.Provider{
		{ID: "oidc", Type: config.ProviderOIDC, Name: "Test IdP", Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret},
		{ID: "github", Type: config.ProviderGitHub, Name: "GitHub", ClientID: gh.ClientID, ClientSecret: gh.ClientSecret, GitHubURL: gh.URL, GitHubAPIURL: gh.URL + "/api"},
	}}}
	if configure != nil {
		configure(cfg)
	}
	config.ApplyDefaults(cfg)

	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	authSvc, err := auth.NewService(st, cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Sandbox.Driver = "fake"
	e := &env{t: t, srv: srv, idp: idp, github: gh, driver: fake.New(t.TempDir())}
	e.driver.RunSandboxd("127.0.0.1", nil)
	e.hub = tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return e.sandboxes.AuthenticateTunnel(ctx, agentID, token)
	}, nil)
	tunnelSrv := httptest.NewServer(e.hub)
	t.Cleanup(tunnelSrv.Close)
	port, _ := strconv.Atoi(tunnelSrv.URL[strings.LastIndex(tunnelSrv.URL, ":")+1:])
	e.sandboxes = sandbox.NewManagerWithOptions(st, e.driver, egress.SpecFunc(st, 15002), sandbox.Options{Tunnel: e.hub, TunnelPort: port, ConnectTimeout: 10 * time.Second}, nil)
	t.Cleanup(func() {
		// Stop in-process sandboxd goroutines.
		rows, _ := st.ListSandboxes(context.Background())
		for _, r := range rows {
			_ = e.driver.Stop(context.Background(), sandbox.ID(r.DriverID))
		}
		e.sandboxes.Close()
	})
	box, _ := secrets.NewBox(bytes.Repeat([]byte{1}, secrets.KeySize))
	ca, err := egress.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e.directory = &egress.Directory{Store: st, Box: box}
	e.logins = &logins.Service{Store: st, Box: box, Audit: &audit.Log{Store: st}, Invalidate: e.directory.Invalidate}
	e.directory.Logins = e.logins
	e.store = st
	e.sessions = session.NewManager(st, nil, nil)
	e.sessions.SetLogins(e.logins)
	e.logins.Restart = e.sessions.Restart
	e.sessions.SetTools(tools.Provider{Store: st, PlatformCommand: sandboxdBin})
	e.hub.HandleIncoming((&platform.Service{Store: st, Sessions: e.sessions, Audit: &audit.Log{Store: st}}).Handle)
	e.clock = &clock{t: time.Now()}
	e.tracker = idle.NewTracker()
	e.tracker.SetClock(e.clock.Now)
	e.sessions.OnActivity(e.tracker.Touch)
	e.scheduler = &trigger.Scheduler{Log: slogDiscard{}, Now: e.clock.Now}
	e.sleeper = &idle.Sleeper{Store: st, Sandboxes: e.sandboxes, Tracker: e.tracker, Busy: e.sessions.AgentBusy, Tunnel: e.hub, Now: e.clock.Now}
	a := api.New(api.Deps{
		Store: st, Auth: authSvc, PublicURL: srv.URL, Sandboxes: e.sandboxes, Tunnel: e.hub, Limits: cfg.Sandbox,
		Box: box, Egress: e.directory, CA: ca, Sessions: e.sessions, Idle: e.sleeper, Audit: &audit.Log{Store: st}, ShowTestHarness: true,
		Triggers: &trigger.Runner{Store: st, Sessions: e.sessions, Audit: &audit.Log{Store: st}}, Logins: e.logins,
	})
	e.sessions.SetWaker(a.Wake)
	e.scheduler.Runner = a.Triggers
	e.sandboxes.OnChange(func(agentID string, p sandbox.Phase) {
		if p == sandbox.PhaseStopping || p == sandbox.PhaseDestroying {
			e.sessions.Disconnect(agentID)
		}
		if p == sandbox.PhaseRunning {
			e.tracker.Touch(agentID)
		}
	})
	t.Cleanup(e.sessions.Close)
	handler = server.New(server.Config{
		Web: fstest.MapFS{"index.html": {Data: []byte("app")}},
		API: a.Mount,
	})
	return e
}

type client struct {
	t    *testing.T
	base string
	http *http.Client
}

func (e *env) anon() *client {
	jar, _ := cookiejar.New(nil)
	return &client{t: e.t, base: e.srv.URL, http: &http.Client{Jar: jar}}
}

// login signs in through the fake OIDC provider and returns a client holding the session.
func (e *env) login(sub, name, email string) *client {
	e.idp.SignInAs(authtest.User{Subject: sub, Name: name, Email: email, EmailVerified: true})
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/oidc?redirect=/after")
	if err != nil {
		e.t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/after" {
		e.t.Fatalf("login ended at %s", res.Request.URL)
	}
	return c
}

func (c *client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequestWithContext(c.t.Context(), method, c.base+"/api/v1"+path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer res.Body.Close()
	if out != nil && res.StatusCode < 300 {
		if err := json.NewDecoder(res.Body).Decode(out); err != nil {
			c.t.Fatalf("%s %s: decode: %v", method, path, err)
		}
	}
	return res.StatusCode
}

func expect(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Fatalf("%s: status %d, want %d", what, got, want)
	}
}

func (c *client) me() gen.User {
	c.t.Helper()
	var m gen.Me
	expect(c.t, "me", c.do("GET", "/me", nil, &m), 200)
	return m.User
}

func TestPublicAndUnauthenticated(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	var providers []gen.AuthProvider
	expect(t, "providers", c.do("GET", "/auth/providers", nil, &providers), 200)
	if len(providers) != 2 || providers[0].Id != "oidc" || providers[1].Type != gen.Github {
		t.Fatalf("providers = %+v", providers)
	}
	expect(t, "me", c.do("GET", "/me", nil, nil), 401)
	expect(t, "agents", c.do("GET", "/agents", nil, nil), 401)
	expect(t, "unknown provider", c.do("GET", "/auth/login/nope", nil, nil), 404)
	expect(t, "local login without auth.local", c.do("POST", "/auth/local", map[string]string{"username": "admin", "password": "x"}, nil), 404)
}

func TestFirstLoginCreatesUser(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice-sub", "Alice", "alice@example.com")
	u := alice.me()
	if u.Name != "Alice" || u.Email != "alice@example.com" {
		t.Fatalf("me = %+v", u)
	}
	var agents []gen.Agent
	expect(t, "agents", alice.do("GET", "/agents", nil, &agents), 200)
	if len(agents) != 0 {
		t.Fatalf("new user has agents: %+v", agents)
	}
	// Second login: same user, profile updated.
	again := e.login("alice-sub", "Alice A.", "alice@example.com").me()
	if again.Id != u.Id || again.Name != "Alice A." {
		t.Fatalf("second login me = %+v", again)
	}
}

func localEnv(t *testing.T, username, pw string) *env {
	t.Helper()
	hash, err := password.New(pw)
	if err != nil {
		t.Fatal(err)
	}
	return newEnvWith(t, func(c *config.Config) {
		c.Auth.Local = &config.Local{Username: username, PasswordHash: hash}
	})
}

func (e *env) localLogin(username, pw string) (*client, int) {
	c := e.anon()
	return c, c.do("POST", "/auth/local", map[string]string{"username": username, "password": pw}, nil)
}

func TestLocalAdminLogin(t *testing.T) {
	e := localEnv(t, "admin", "s3cret pw")
	var providers []gen.AuthProvider
	expect(t, "providers", e.anon().do("GET", "/auth/providers", nil, &providers), 200)
	if len(providers) != 3 || providers[0].Type != gen.Local || providers[0].Id != "local" {
		t.Fatalf("providers = %+v", providers)
	}

	for _, bad := range []struct{ user, pw string }{{"admin", "wrong"}, {"Admin", "s3cret pw"}, {"", ""}, {"admin", "s3cret pw "}} {
		c, status := e.localLogin(bad.user, bad.pw)
		expect(t, "bad credentials "+bad.user+"/"+bad.pw, status, 401)
		expect(t, "no session after failure", c.do("GET", "/me", nil, nil), 401)
	}
	res := e.anon().raw("POST", "/auth/local", strings.NewReader("not json"))
	res.Body.Close()
	expect(t, "malformed body", res.StatusCode, 400)

	admin, status := e.localLogin("admin", "s3cret pw")
	expect(t, "login", status, 204)
	u := admin.me()
	if u.Name != "admin" || u.Email != "" {
		t.Fatalf("me = %+v", u)
	}
	// The same local user every time.
	again, _ := e.localLogin("admin", "s3cret pw")
	if again.me().Id != u.Id {
		t.Fatal("second login created another user")
	}
	// The local admin is a normal user: it creates and owns agents.
	var ag gen.Agent
	expect(t, "create agent", admin.do("POST", "/agents", gen.AgentInput{Name: "mine", Harness: ptr("fake")}, &ag), 201)
	e.sandboxes.Wait()
	if ag.Role != gen.Owner || ag.Owner.Id != u.Id {
		t.Fatalf("agent = %+v", ag)
	}
	expect(t, "logout", admin.do("POST", "/auth/logout", nil, nil), 204)
	expect(t, "me after logout", admin.do("GET", "/me", nil, nil), 401)
}

func TestLocalAdminLoginRateLimited(t *testing.T) {
	e := localEnv(t, "admin", "pw")
	limited := false
	for i := 0; i < 12 && !limited; i++ {
		_, status := e.localLogin("admin", "guess")
		switch status {
		case 401:
		case 429:
			limited = true
		default:
			t.Fatalf("attempt %d: status %d", i, status)
		}
	}
	if !limited {
		t.Fatal("never rate limited")
	}
	// Even the right password is refused until the window passes.
	_, status := e.localLogin("admin", "pw")
	expect(t, "right password while limited", status, 429)
}

func TestNoSignInConfigured(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Providers = nil })
	var providers []gen.AuthProvider
	expect(t, "providers", e.anon().do("GET", "/auth/providers", nil, &providers), 200)
	if len(providers) != 0 {
		t.Fatalf("providers = %+v", providers)
	}
	_, status := e.localLogin("admin", "admin")
	expect(t, "local login", status, 404)
}

func TestGitHubLogin(t *testing.T) {
	e := newEnv(t)
	e.github.SignInAs(authtest.GitHubUser{ID: 4242, Login: "octo", Name: "", PrimaryEmail: "octo@example.com", EmailVerified: true})
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/login/github")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/" {
		t.Fatalf("ended at %s", res.Request.URL)
	}
	var me gen.Me
	expect(t, "me", c.do("GET", "/me", nil, &me), 200)
	if me.User.Name != "octo" || me.User.Email != "octo@example.com" || !strings.Contains(me.User.AvatarUrl, "octo") {
		t.Fatalf("me = %+v", me)
	}
}

func TestLoginRejectsBadStateAndOpenRedirect(t *testing.T) {
	e := newEnv(t)
	c := e.anon()
	res, err := c.http.Get(c.base + "/api/v1/auth/callback/oidc?code=x&state=forged")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.Request.URL.Path != "/login" || res.Request.URL.Query().Get("error") == "" {
		t.Fatalf("forged state ended at %s", res.Request.URL)
	}
	expect(t, "no session", c.do("GET", "/me", nil, nil), 401)

	e.idp.SignInAs(authtest.User{Subject: "s", Name: "S"})
	for _, target := range []string{"//evil.example", "https://evil.example/", "/\\evil.example"} {
		res, err = c.http.Get(c.base + "/api/v1/auth/login/oidc?redirect=" + target)
		if err != nil {
			t.Fatal(err)
		}
		_ = res.Body.Close()
		if res.Request.URL.Host != strings.TrimPrefix(e.srv.URL, "http://") || res.Request.URL.Path != "/" {
			t.Fatalf("redirect %q ended at %s", target, res.Request.URL)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv(t)
	alice := e.login("alice", "Alice", "a@example.com")
	expect(t, "me", alice.do("GET", "/me", nil, nil), 200)
	expect(t, "logout", alice.do("POST", "/auth/logout", nil, nil), 204)
	expect(t, "me after logout", alice.do("GET", "/me", nil, nil), 401)
}
