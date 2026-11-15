// Command agenthold is the agenthold server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	_ "github.com/gempir/agenthold/internal/harness/claude"
	_ "github.com/gempir/agenthold/internal/harness/codex"
	fakeharness "github.com/gempir/agenthold/internal/harness/fake"
	_ "github.com/gempir/agenthold/internal/harness/opencode"
	"github.com/gempir/agenthold/internal/idle"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/sandbox/firecracker"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tools"
	"github.com/gempir/agenthold/internal/trigger"
	"github.com/gempir/agenthold/internal/tunnel"
	"github.com/gempir/agenthold/internal/version"
	"github.com/gempir/agenthold/web"
)

func main() {
	if len(os.Args) > 1 {
		var err error
		switch os.Args[1] {
		case "backup":
			err = runBackup(os.Args[2:])
		case "restore":
			err = runRestore(os.Args[2:])
		case "healthcheck":
			err = healthcheck()
		case "hash-password":
			err = runHashPassword(os.Args[2:])
		case "install":
			err = runInstall(os.Args[2:])
		default:
			goto server
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
server:
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Printf("agenthold %s (%s)\n", version.Version, version.Commit)
		return nil
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	st, err := store.Open(ctx, filepath.Join(cfg.DataDir, "agenthold.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	authSvc, err := auth.NewService(st, cfg)
	if err != nil {
		return err
	}
	if !cfg.Auth.SignInConfigured() {
		logger.Warn("nobody can sign in: no sign-in method is configured. Add a local admin " +
			"(auth.local with username and a password_hash from `agenthold hash-password`) " +
			"or an identity provider (auth.providers, OIDC or GitHub) to the config file")
	}
	tunnelPort, err := portOf(cfg.Sandbox.TunnelListen)
	if err != nil {
		return fmt.Errorf("sandbox.tunnel_listen: %w", err)
	}

	key, generated, err := secrets.LoadMasterKey(cfg.Secrets.MasterKeyFile)
	if err != nil {
		return fmt.Errorf("master key: %w", err)
	}
	if generated {
		logger.Warn("generated a new master key; back it up, secrets cannot be decrypted without it", "file", cfg.Secrets.MasterKeyFile)
	}
	box, err := secrets.NewBox(key)
	if err != nil {
		return err
	}
	ca, err := egress.LoadOrCreateCA(filepath.Join(cfg.DataDir, "egress"))
	if err != nil {
		return fmt.Errorf("egress CA: %w", err)
	}
	directory := &egress.Directory{Store: st, Box: box, Log: logger}
	auditLog := &audit.Log{Store: st, Log: logger}
	// Account logins: delivered to harnesses, injected by the proxy, refreshed here.
	loginSvc := &logins.Service{Store: st, Box: box, Audit: auditLog, Log: logger, Invalidate: directory.Invalidate}
	directory.Logins = loginSvc
	egressOn := !cfg.Egress.Disabled

	fw := &egress.Firewall{
		Pool:      netip.MustParsePrefix(cfg.Sandbox.NetworkPool),
		ProxyPort: cfg.Egress.ProxyPort, DNSPort: cfg.Egress.DNSPort,
		OpenPorts: []int{tunnelPort, cfg.Egress.ExplicitPort},
	}
	var localNode *node.Local
	driver, err := newDriver(ctx, cfg, fw, func(ctx context.Context, id sandbox.ID) error {
		return node.Client(localNode, string(id)).Shutdown(ctx)
	}, logger)
	if err != nil {
		return err
	}
	hub := tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return localNode.AuthenticateTunnel(ctx, agentID, token)
	}, logger)
	localNode = node.NewLocal(driver, hub)
	localNode.SetCA(ca)
	if cfg.Sandbox.Driver == "firecracker" {
		if err := fw.Install(); err != nil {
			return fmt.Errorf("guest firewall: %w", err)
		}
	}
	if egressOn {
		if err := startEgress(ctx, cfg, ca, localNode.ProxyAgents(), logger); err != nil {
			return fmt.Errorf("egress proxy: %w", err)
		}
	}
	pusher := &node.ConfigPusher{Store: st, Directory: directory, Node: localNode}
	directory.OnInvalidate = func() {
		if err := pusher.Sync(context.Background()); err != nil {
			logger.Error("push proxy configuration", "err", err)
			_ = localNode.SetAgentConfigs(context.Background(), nil)
		}
	}
	if err := pusher.Sync(ctx); err != nil {
		return err
	}
	go func() { _ = pusher.ServeIntercepts(ctx) }()
	sandboxes := sandbox.NewManagerWithNode(st, localNode, egress.SpecFunc(st, cfg.Egress.ExplicitPort), sandbox.Options{
		TunnelPort: tunnelPort,
	}, logger)
	sandboxes.OnChange(func(string, sandbox.Phase) { directory.Invalidate() })
	defer sandboxes.Close()

	tunnelSrv := &http.Server{Addr: cfg.Sandbox.TunnelListen, Handler: tunnelMux(hub), ReadHeaderTimeout: 10 * time.Second}
	tunnelLn, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Sandbox.TunnelListen)
	if err != nil {
		return fmt.Errorf("tunnel listener: %w", err)
	}
	go func() {
		if err := tunnelSrv.Serve(tunnelLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("tunnel listener", "err", err)
		}
	}()
	defer tunnelSrv.Close()
	if err := sandboxes.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile sandboxes: %w", err)
	}

	sessions := session.NewManager(st, nil, logger)
	sessions.SetLogins(loginSvc)
	loginSvc.Restart = sessions.Restart
	go loginSvc.Run(ctx)
	runner := &trigger.Runner{Store: st, Sessions: sessions, Audit: auditLog}
	go (&trigger.Scheduler{Runner: runner, Log: logger}).Run(ctx)
	platformCmd := sandbox.SandboxdDir + "/sandboxd"
	if cfg.Sandbox.Driver == "fake" {
		platformCmd = cfg.Sandbox.SandboxdPath
	}
	sessions.SetTools(tools.Provider{Store: st, PlatformCommand: platformCmd})
	platformSvc := &platform.Service{Store: st, Sessions: sessions, Audit: auditLog, Log: logger}
	go func() {
		for {
			info, stream, err := localNode.NextIncoming(ctx)
			if err != nil {
				return
			}
			go func() { defer stream.Close(); platformSvc.Handle(ctx, info.AgentID, stream, info.Open) }()
		}
	}()
	defer sessions.Close()
	tracker := idle.NewTracker()
	sessions.OnActivity(tracker.Touch)
	sleeper := &idle.Sleeper{
		Store: st, Sandboxes: sandboxes, Tracker: tracker, Busy: sessions.AgentBusy, Node: localNode,
		Log: logger, Interval: cfg.Sandbox.IdleCheckInterval,
	}
	go sleeper.Run(ctx)
	sandboxes.OnChange(func(agentID string, p sandbox.Phase) {
		if p == sandbox.PhaseRunning {
			tracker.Touch(agentID) // a fresh boot starts the idle clock
		}
	})
	sandboxes.OnChange(func(agentID string, p sandbox.Phase) {
		if p == sandbox.PhaseStopping || p == sandbox.PhaseDestroying || p == sandbox.PhaseRebuilding {
			sessions.Disconnect(agentID)
		}
	})

	apiSrv := api.New(api.Deps{
		Store: st, Auth: authSvc, PublicURL: cfg.PublicURL, Sandboxes: sandboxes, Node: localNode, Limits: cfg.Sandbox,
		Box: box, Egress: directory, Sessions: sessions, Idle: sleeper, Audit: auditLog, Triggers: runner, Logins: loginSvc,
		ShowTestHarness: cfg.TestHarness || cfg.Sandbox.Driver == "fake",
	})
	sessions.SetWaker(apiSrv.Wake)

	srvCfg := server.Config{Web: web.Dist(), Logger: logger, API: apiSrv.Mount}
	if cfg.DevProxy != "" {
		u, err := url.Parse(cfg.DevProxy)
		if err != nil {
			return fmt.Errorf("parse dev proxy url: %w", err)
		}
		srvCfg.DevProxy = u
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(srvCfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", cfg.Listen, "public_url", cfg.PublicURL, "version", version.Version)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// startEgress starts node-local proxy and DNS listeners. The firewall is
// installed independently, including when egress is disabled (fail closed).
func startEgress(ctx context.Context, cfg *config.Config, ca *egress.CA, dir egress.Agents, logger *slog.Logger) (err error) {
	eg := cfg.Egress
	names := egress.NewNameCache()
	proxy := &egress.Proxy{CA: ca, Agents: dir, Names: names, OriginalDst: egress.OriginalDst, Logger: logger, GuestPool: netip.MustParsePrefix(cfg.Sandbox.NetworkPool)}
	upstream := eg.DNSUpstream
	if upstream == "" {
		upstream = egress.UpstreamFromResolvConf()
	}
	dns := &egress.DNS{Agents: dir, Names: names, Upstream: upstream}
	lc := net.ListenConfig{}
	var listeners []io.Closer
	closeAll := func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}
	defer func() {
		if err != nil {
			closeAll()
		}
	}()
	listen := func(port int) (net.Listener, error) {
		ln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(port))
		if err == nil {
			listeners = append(listeners, ln)
		}
		return ln, err
	}
	tln, err := listen(eg.ProxyPort)
	if err != nil {
		return err
	}
	eln, err := listen(eg.ExplicitPort)
	if err != nil {
		return err
	}
	dln, err := listen(eg.DNSPort)
	if err != nil {
		return err
	}
	dpc, err := lc.ListenPacket(ctx, "udp", ":"+strconv.Itoa(eg.DNSPort))
	if err != nil {
		return err
	}
	listeners = append(listeners, dpc)
	go func() { <-ctx.Done(); closeAll() }()
	go func() { _ = proxy.ServeTransparent(tln) }()
	go func() {
		_ = (&http.Server{Handler: proxy.ExplicitHandler(), ReadHeaderTimeout: 30 * time.Second}).Serve(eln)
	}()
	go func() { _ = dns.ServeTCP(dln) }()
	go func() { _ = dns.ServeUDP(dpc) }()
	logger.Info("egress: proxy listening", "dns_upstream", upstream)
	return nil
}

func newDriver(ctx context.Context, cfg *config.Config, fw *egress.Firewall, shutdown func(context.Context, sandbox.ID) error, logger *slog.Logger) (sandbox.Driver, error) {
	switch cfg.Sandbox.Driver {
	case "fake":
		logger.Warn("using the fake sandbox driver: terminals run on this machine as the server's user")
		fakeharness.Command = filepath.Join(filepath.Dir(cfg.Sandbox.SandboxdPath), "fakeagent")
		d := fake.New(cfg.Sandbox.FakeDir)
		d.RunSandboxd("127.0.0.1", logger)
		return d, nil
	case "firecracker":
		fc := cfg.Sandbox.Firecracker
		paths := []*string{&fc.Binary, &fc.Jailer, &fc.Kernel, &fc.BaseImage}
		for _, p := range paths {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return nil, err
			}
			*p = abs
		}
		dataDir, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		return firecracker.New(ctx, firecracker.Config{
			DataDir: dataDir, BaseImagePath: fc.BaseImage, KernelPath: fc.Kernel,
			FirecrackerPath: fc.Binary, JailerPath: fc.Jailer,
			NetworkPool:      netip.MustParsePrefix(cfg.Sandbox.NetworkPool),
			OverlaySizeBytes: fc.OverlayGB << 30, HomeSizeBytes: fc.HomeGB << 30,
			Shutdown: shutdown, RegisterNetwork: fw.Register, UnregisterNetwork: fw.Unregister,
		})
	default:
		return nil, fmt.Errorf("unknown sandbox driver %q", cfg.Sandbox.Driver)
	}
}

func tunnelMux(hub *tunnel.Hub) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /tunnel", hub)
	return mux
}

func portOf(addr string) (int, error) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(p)
}

// healthcheck is for container health checks (the image has no curl).
func healthcheck() error {
	addr := envOr("AGENTHOLD_LISTEN", ":8080")
	if strings.HasPrefix(addr, ":") {
		addr = "127.0.0.1" + addr
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
