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
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/dl"
	"github.com/gempir/agenthold/internal/egress"
	_ "github.com/gempir/agenthold/internal/harness/claude"
	_ "github.com/gempir/agenthold/internal/harness/codex"
	_ "github.com/gempir/agenthold/internal/harness/opencode"
	"github.com/gempir/agenthold/internal/idle"
	"github.com/gempir/agenthold/internal/logins"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/platform"
	"github.com/gempir/agenthold/internal/preview"
	"github.com/gempir/agenthold/internal/runtimeassets"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/firecracker"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
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
		case "node":
			err = runNode(os.Args[2:])
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
	driverName := cfg.Sandbox.LocalDriver(firecracker.KVMPresent())
	cfg.Sandbox.Driver = driverName
	st, err := store.Open(ctx, cfg.Database.URL)
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

	// The built-in node exists only when this host runs VMs (or uses the fake driver).
	clusterCfg := node.ClusterConfig{Store: st, CA: ca, Log: logger}
	if driverName != "none" {
		host, err := startLocalHost(ctx, cfg, driverName, runtimeassets.Controller{}, logger)
		if err != nil {
			return err
		}
		if err := host.useCA(ctx, ca); err != nil {
			return err
		}
		clusterCfg.Local = host.node
		clusterCfg.LocalName, _ = os.Hostname()
	} else {
		logger.Info("no local node (sandbox.driver resolved to none): agents run on edge nodes only")
	}
	var nodeIdentity *enrollment.Identity
	if cfg.Nodes.Listen != config.NodesDisabled {
		nodeIdentity, err = enrollment.LoadOrCreateIdentity(filepath.Join(cfg.DataDir, "nodes", "controller.pem"), "agenthold-controller")
		if err != nil {
			return fmt.Errorf("node listener identity: %w", err)
		}
		clusterCfg.Endpoint, clusterCfg.Fingerprint = cfg.Nodes.Advertise, nodeIdentity.Fingerprint
	}
	release, err := runtimeassets.Embedded()
	if err != nil {
		return fmt.Errorf("embedded release: %w", err)
	}
	downloads := dl.New(dl.Config{DataDir: cfg.DataDir, Release: release, Log: logger})
	downloads.Start(ctx)
	cluster, err := node.NewCluster(ctx, clusterCfg)
	if err != nil {
		return err
	}
	pusher := &node.ConfigPusher{Store: st, Directory: directory, Node: cluster}
	directory.OnInvalidate = func() {
		if err := pusher.Sync(context.Background()); err != nil {
			logger.Error("push proxy configuration", "err", err)
			_ = cluster.SetAgentConfigs(context.Background(), nil)
		}
	}
	if err := pusher.Sync(ctx); err != nil {
		return err
	}
	go func() { _ = pusher.ServeIntercepts(ctx) }()
	specForAgent := egress.SpecFunc(st, cfg.Egress.ExplicitPort)
	if driverName == "firecracker" {
		baseSpec := specForAgent
		baseRevision := cfg.Sandbox.Firecracker.BaseImage
		specForAgent = func(a db.Agent) sandbox.Spec {
			s := baseSpec(a)
			s.Image = baseRevision
			return s
		}
	}
	// Services reach every machine in its boot files.
	appsMgr := &apps.Manager{Store: st, Node: cluster, Audit: auditLog, Limits: cfg.Apps, Log: logger}
	sandboxes := sandbox.NewManagerWithNode(st, cluster, specForAgent, sandbox.Options{
		TunnelPort: tunnelPort, BootFiles: appsMgr.BootFiles,
	}, logger)
	appsMgr.Sandboxes = sandboxes
	sandboxes.OnChange(func(string, sandbox.Phase) { directory.Invalidate() })
	defer sandboxes.Close()
	cluster.OnConnect(func(ctx context.Context, nodeID string) {
		if err := pusher.Sync(ctx); err != nil {
			logger.Error("push proxy configuration", "node", nodeID, "err", err)
		}
		if err := sandboxes.ReconcileNode(ctx, nodeID); err != nil {
			logger.Error("reconcile node", "node", nodeID, "err", err)
		}
	})
	if err := sandboxes.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile sandboxes: %w", err)
	}
	if nodeIdentity != nil {
		ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Nodes.Listen)
		if err != nil {
			return fmt.Errorf("node listener: %w", err)
		}
		nodeSrv := &enrollment.Server{
			Identity: nodeIdentity, Registry: cluster, Log: logger, Version: version.Version,
			Handle: func(ctx context.Context, p enrollment.Peer, conn net.Conn) { cluster.ServePeer(ctx, p, conn) },
		}
		if release != nil {
			if root, why := dl.AdvertisedURL(cfg.PublicURL); root != "" {
				nodeSrv.DownloadURL = root
			} else {
				logger.Warn("not telling edge nodes where to download their base image from: "+why, "public_url", cfg.PublicURL)
			}
		}
		go func() {
			if err := nodeSrv.Serve(ctx, ln); err != nil && ctx.Err() == nil {
				logger.Error("node listener", "err", err)
			}
		}()
		logger.Info("edge node listener", "addr", cfg.Nodes.Listen, "advertise", cfg.Nodes.Advertise, "fingerprint", nodeIdentity.Fingerprint)
	}

	sessions := session.NewManager(st, nil, logger)
	sessions.SetLogins(loginSvc)
	sessions.SetModelProbe(true)
	loginSvc.Restart = sessions.Restart
	go loginSvc.Run(ctx)
	runner := &trigger.Runner{Store: st, Sessions: sessions, Audit: auditLog}
	go (&trigger.Scheduler{Runner: runner, Log: logger}).Run(ctx)
	platformCmd := sandbox.SandboxdDir + "/sandboxd"
	if driverName == "fake" {
		platformCmd = cfg.Sandbox.SandboxdPath
	}
	sessions.SetTools(tools.Provider{Store: st, PlatformCommand: platformCmd})
	previewSvc := &preview.Service{Store: st, Hosts: preview.NewHosts(cfg.Preview.HostSuffix, cfg.PublicURL)}
	var identity *preview.Signer
	if previewSvc.Enabled() {
		identity, err = preview.LoadOrCreateSigner(filepath.Join(cfg.DataDir, "preview", "identity.key"))
		if err != nil {
			return fmt.Errorf("preview identity key: %w", err)
		}
		logger.Info("preview URLs enabled", "host_suffix", cfg.Preview.HostSuffix)
		if u, err := url.Parse(cfg.PublicURL); err == nil && config.PreviewSameSite(cfg.Preview.HostSuffix, u.Hostname()) {
			logger.Warn("preview hosts share a registrable domain with public_url: apps can make same-site requests to agenthold; "+
				"use a separate domain for full isolation", "host_suffix", cfg.Preview.HostSuffix, "public_url", cfg.PublicURL)
		}
		go previewSvc.Run(ctx, 10*time.Minute, logger)
	}
	appsMgr.Preview = previewSvc
	platformSvc := &platform.Service{Store: st, Sessions: sessions, Audit: auditLog, Log: logger, Preview: previewSvc, Apps: appsMgr}
	go func() {
		for {
			info, stream, err := cluster.NextIncoming(ctx)
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
		Store: st, Sandboxes: sandboxes, Tracker: tracker, Busy: sessions.AgentBusy, Node: cluster,
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
		Store: st, Auth: authSvc, PublicURL: cfg.PublicURL, Sandboxes: sandboxes, Node: cluster, Nodes: cluster, Limits: cfg.Sandbox,
		Box: box, Egress: directory, Sessions: sessions, Idle: sleeper, Audit: auditLog, Triggers: runner, Logins: loginSvc,
		Preview: previewSvc, Identity: identity, Apps: appsMgr,
		ShowTestHarness: cfg.TestHarness || driverName == "fake",
	})
	sessions.SetWaker(apiSrv.Wake)
	appsMgr.Wake = apiSrv.Wake
	sandboxes.SetBusy(sessions.AgentBusy)

	srvCfg := server.Config{Web: web.Dist(), Logger: logger, API: apiSrv.Mount, DL: downloads}
	if cfg.DevProxy != "" {
		u, err := url.Parse(cfg.DevProxy)
		if err != nil {
			return fmt.Errorf("parse dev proxy url: %w", err)
		}
		srvCfg.DevProxy = u
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           apiSrv.PreviewHandler(server.New(srvCfg)),
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
