package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
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

	"github.com/gempir/agenthold/internal/activity"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
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
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/password"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/preview"
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
	activity  *activity.Log
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
	// edge is set when AGENTHOLD_TEST_NODE=remote.
	edge *edgeNode
	// Previews are served at <name>.apps.test on the test server.
	preview  *preview.Service
	identity *preview.Signer
	platform *platform.Service
	apps     *apps.Manager

	// api, auth and box are the in-process API and its services (the Slack
	// integration calls the handlers directly).
	api  *api.API
	auth *auth.Service
	box  *secrets.Box

	statsMu sync.Mutex
	statsFn api.StatsFunc
}

// setStats replaces how the admin overview reads machine stats (nil: ask the
// sandboxd through the node).
func (e *env) setStats(fn api.StatsFunc) {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	e.statsFn = fn
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

// sandboxdBin runs the platform MCP server ("sandboxd mcp") for fake sandboxes: the agenthold
// binary, named sandboxd so that it starts in that role.
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
	fakeharness.SetCommand(bin)
	sandboxdBin = filepath.Join(dir, "sandboxd")
	if out, err := exec.Command("go", "build", "-o", sandboxdBin, "github.com/gempir/agenthold/cmd/agenthold").CombinedOutput(); err != nil {
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
	goroutinesBefore := goroutines()
	idp := authtest.NewIdP(t)
	gh := authtest.NewGitHub(t)

	var handler http.Handler
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.ServeHTTP(w, r) }))
	t.Cleanup(srv.Close)

	cfg := &config.Config{PublicURL: srv.URL, Auth: config.Auth{Providers: []config.Provider{
		{ID: "oidc", Type: config.ProviderOIDC, Name: "Test IdP", Issuer: idp.URL, ClientID: idp.ClientID, ClientSecret: idp.ClientSecret},
		{ID: "github", Type: config.ProviderGitHub, Name: "GitHub", ClientID: gh.ClientID, ClientSecret: gh.ClientSecret, GitHubURL: gh.URL, GitHubAPIURL: gh.URL + "/api"},
	}}}
	// Test users get the User role without a quota unless a test configures
	// roles itself (the shipped default is Disabled with 2 agents).
	cfg.Auth.DefaultRole = config.RoleUser
	cfg.Auth.UserMaxAgents = new(1000)
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
	noGoroutinesLeft(t, goroutinesBefore)
	e.driver.RunSandboxd("127.0.0.1", nil)
	var localNode *node.Local
	e.hub = tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return localNode.AuthenticateTunnel(ctx, agentID, token)
	}, nil)
	tunnelSrv := httptest.NewServer(e.hub)
	t.Cleanup(tunnelSrv.Close)
	port, _ := strconv.Atoi(tunnelSrv.URL[strings.LastIndex(tunnelSrv.URL, ":")+1:])
	localNode = node.NewLocal(e.driver, e.hub)
	localNode.SetDataDir(t.TempDir())
	box, _ := secrets.NewBox(bytes.Repeat([]byte{1}, secrets.KeySize))
	ca, err := egress.LoadOrCreateCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// AGENTHOLD_TEST_NODE=remote runs every API test against an edge node:
	// the same fake node, joined over TLS and served over the node protocol.
	var n node.Node = localNode
	var nodes api.NodeAdmin
	if os.Getenv("AGENTHOLD_TEST_NODE") == "remote" {
		e.edge = joinEdgeNode(t, st, localNode, ca)
		n, nodes = e.edge.cluster, e.edge.cluster
	} else {
		localNode.SetCA(ca)
	}
	e.apps = &apps.Manager{Store: st, Node: n, Audit: &audit.Log{Store: st}, Limits: cfg.Apps}
	e.activity = activity.New()
	activityLog := slog.New(activity.Tap(slog.DiscardHandler, e.activity))
	e.sandboxes = sandbox.NewManagerWithNode(st, n, egress.SpecFunc(st, 15002), sandbox.Options{TunnelPort: port, ConnectTimeout: 10 * time.Second, BootFiles: e.apps.BootFiles, LastActivity: e.activity.Last}, activityLog)
	e.apps.Sandboxes = e.sandboxes
	t.Cleanup(func() {
		// Finish the background operations first: one still starting a
		// sandbox would otherwise start its in-process sandboxd after the
		// stop below, and that keeps writing into the temp directories
		// while they are removed.
		e.sandboxes.Close()
		// Stop in-process sandboxd goroutines.
		rows, _ := st.ListSandboxes(context.Background())
		for _, r := range rows {
			_ = e.driver.Stop(context.Background(), sandbox.ID(r.DriverID))
		}
	})
	e.directory = &egress.Directory{Store: st, Box: box}
	e.logins = &logins.Service{Store: st, Box: box, Audit: &audit.Log{Store: st}, Invalidate: e.directory.Invalidate}
	e.directory.Logins = e.logins
	e.store = st
	e.sessions = session.NewManager(st, nil, activityLog)
	e.sessions.SetLogins(e.logins)
	e.sessions.SetAudit(&audit.Log{Store: st})
	e.logins.Restart = e.sessions.Restart
	e.sessions.SetTools(tools.Provider{Store: st, PlatformCommand: sandboxdBin})
	e.preview = &preview.Service{Store: st, Hosts: preview.NewHosts(".apps.test", srv.URL)}
	if e.identity, err = preview.LoadOrCreateSigner(filepath.Join(t.TempDir(), "identity.key")); err != nil {
		t.Fatal(err)
	}
	e.apps.Preview = e.preview
	platformSvc := &platform.Service{Store: st, Sessions: e.sessions, Audit: &audit.Log{Store: st}, Preview: e.preview, Apps: e.apps}
	e.platform = platformSvc
	go func() {
		for {
			info, stream, err := n.NextIncoming(t.Context())
			if err != nil {
				return
			}
			go func() { defer stream.Close(); platformSvc.Handle(t.Context(), info.AgentID, stream, info.Open) }()
		}
	}()
	e.clock = &clock{t: time.Now()}
	e.tracker = idle.NewTracker()
	e.tracker.SetClock(e.clock.Now)
	e.sessions.OnActivity(e.tracker.Touch)
	e.scheduler = &trigger.Scheduler{Log: slogDiscard{}, Now: e.clock.Now}
	e.sleeper = &idle.Sleeper{Store: st, Sandboxes: e.sandboxes, Tracker: e.tracker, Busy: e.sessions.AgentBusy, Node: n, Now: e.clock.Now}
	a := api.New(api.Deps{
		Stats: func(ctx context.Context, agentID string) (tunnel.Stats, error) {
			e.statsMu.Lock()
			fn := e.statsFn
			e.statsMu.Unlock()
			if fn != nil {
				return fn(ctx, agentID)
			}
			return node.Client(n, agentID).Stats(ctx)
		},
		Store: st, Auth: authSvc, PublicURL: srv.URL, Sandboxes: e.sandboxes, Node: n, Nodes: nodes, Limits: cfg.Sandbox,
		Box: box, Egress: e.directory, Sessions: e.sessions, Idle: e.sleeper, Audit: &audit.Log{Store: st}, ShowTestHarness: true,
		Triggers: &trigger.Runner{Store: st, Sessions: e.sessions, Audit: &audit.Log{Store: st}}, Logins: e.logins,
		Preview: e.preview, Identity: e.identity, Apps: e.apps, Activity: e.activity, UserMaxAgents: cfg.Auth.UserMaxAgents,
	})
	e.api, e.auth, e.box = a, authSvc, box
	e.sessions.SetWaker(a.Wake)
	e.apps.Wake = a.Wake
	e.sandboxes.SetBusy(e.sessions.AgentBusy)
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
	handler = a.PreviewHandler(server.New(server.Config{
		Web: fstest.MapFS{"index.html": {Data: []byte("app")}},
		API: a.Mount,
	}))
	return e
}

// edgeNode is the controller and node side of an edge node in tests.
type edgeNode struct {
	t         *testing.T
	ctx       context.Context
	cluster   *node.Cluster
	backend   *node.Local
	state     *enrollment.State
	connected chan string
	stop      context.CancelFunc
	served    chan struct{}
}

// serve runs the node side of conn until down is called.
func (n *edgeNode) serve(conn net.Conn) {
	ctx, stop := context.WithCancel(n.ctx)
	n.stop, n.served = stop, make(chan struct{})
	go func() {
		defer close(n.served)
		_ = node.ServeRemote(ctx, conn, n.backend, node.ServeOptions{SetEgressCA: func(cert, key []byte) error {
			pushed, err := egress.ParseCA(cert, key)
			if err == nil {
				n.backend.SetCA(pushed)
			}
			return err
		}})
	}()
	select {
	case <-n.connected:
	case <-time.After(10 * time.Second):
		n.t.Fatal("edge node did not connect")
	}
}

// down drops the node's connection; its fake sandboxes keep running.
func (n *edgeNode) down() {
	n.stop()
	<-n.served
	deadline := time.Now().Add(5 * time.Second)
	for {
		infos, _ := n.cluster.Nodes(n.ctx)
		if len(infos) == 1 && !infos[0].Connected {
			return
		}
		if time.Now().After(deadline) {
			n.t.Fatal("controller did not notice the disconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// up reconnects with the identity saved at join time.
func (n *edgeNode) up() {
	conn, err := enrollment.Connect(n.ctx, n.state)
	if err != nil {
		n.t.Fatal(err)
	}
	n.serve(conn)
}

// joinEdgeNode makes backend an edge node of a fresh cluster: it joins with a
// token over loopback TLS and serves the node protocol until the test ends.
func joinEdgeNode(t *testing.T, st *store.Store, backend *node.Local, ca *egress.CA) *edgeNode {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel) // registered first, so it runs after the sandboxes stopped
	identity, err := enrollment.NewIdentity("test-controller")
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cluster, err := node.NewCluster(ctx, node.ClusterConfig{Store: st, CA: ca, Endpoint: ln.Addr().String(), Fingerprint: identity.Fingerprint})
	if err != nil {
		t.Fatal(err)
	}
	connected := make(chan string, 4)
	cluster.OnConnect(func(_ context.Context, id string) { connected <- id })
	srv := &enrollment.Server{
		Identity: identity, Registry: cluster, Version: "v-test", DownloadURL: "https://controller.example/dl/",
		Handle: func(ctx context.Context, p enrollment.Peer, conn net.Conn) { cluster.ServePeer(ctx, p, conn) },
	}
	go func() { _ = srv.Serve(ctx, ln) }()
	jt, err := cluster.CreateJoinToken(ctx, "admin", "edge", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := enrollment.ParseToken(jt.Token)
	if err != nil {
		t.Fatal(err)
	}
	conn, state, err := enrollment.Join(ctx, filepath.Join(t.TempDir(), "state.json"), tok, "edge")
	if err != nil {
		t.Fatal(err)
	}
	if state.DownloadURL != "https://controller.example/dl/" || state.ControllerVersion != "v-test" {
		t.Fatalf("a joined node must learn where its controller serves downloads: %+v", state)
	}
	n := &edgeNode{t: t, ctx: ctx, cluster: cluster, backend: backend, state: state, connected: connected}
	n.serve(conn)
	return n
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
