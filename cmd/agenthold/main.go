// Command agenthold is the agenthold server.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
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
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/docker"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/session"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/tunnel"
	"github.com/gempir/agenthold/internal/version"
	"github.com/gempir/agenthold/web"
)

func main() {
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
	if len(cfg.Auth.Providers) == 0 {
		logger.Warn("no auth providers configured; nobody can sign in")
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
	egressOn := !cfg.Egress.Disabled
	if egressOn {
		if err := startEgress(ctx, cfg, ca, directory, st, tunnelPort, logger); err != nil {
			logger.Error("egress proxy unavailable: sandboxes have no network access", "err", err)
			egressOn = false
		}
	}

	driver, err := newDriver(ctx, cfg, egressOn, logger)
	if err != nil {
		return err
	}
	var sandboxes *sandbox.Manager
	hub := tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return sandboxes.AuthenticateTunnel(ctx, agentID, token)
	}, logger)
	sandboxes = sandbox.NewManagerWithOptions(st, driver, egress.SpecFunc(st, cfg.Egress.ExplicitPort), sandbox.Options{
		Tunnel: hub, TunnelPort: tunnelPort,
		BootFiles: func(context.Context, string) (map[string][]byte, error) { return ca.BootFiles(), nil },
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
	defer sessions.Close()
	tracker := idle.NewTracker()
	sessions.OnActivity(tracker.Touch)
	sleeper := &idle.Sleeper{
		Store: st, Sandboxes: sandboxes, Tracker: tracker, Busy: sessions.AgentBusy, Tunnel: hub,
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
		Store: st, Auth: authSvc, PublicURL: cfg.PublicURL, Sandboxes: sandboxes, Tunnel: hub, Limits: cfg.Sandbox,
		Box: box, Egress: directory, CA: ca, Sessions: sessions, Idle: sleeper, Audit: &audit.Log{Store: st, Log: logger},
		ShowTestHarness: cfg.Sandbox.Driver == "fake" || cfg.DevProxy != "",
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

// startEgress starts the proxy, DNS and explicit proxy listeners and installs
// the firewall that redirects sandbox traffic to them.
func startEgress(ctx context.Context, cfg *config.Config, ca *egress.CA, dir *egress.Directory, st *store.Store, tunnelPort int, logger *slog.Logger) error {
	eg := cfg.Egress
	rec := &egress.Recorder{Store: st, Keep: eg.LogKeep}
	names := egress.NewNameCache()
	proxy := &egress.Proxy{CA: ca, Agents: dir, Names: names, Log: rec.Record, OriginalDst: egress.OriginalDst, Logger: logger}
	upstream := eg.DNSUpstream
	if upstream == "" {
		upstream = egress.UpstreamFromResolvConf()
	}
	dns := &egress.DNS{Agents: dir, Names: names, Upstream: upstream, Log: rec.Record}

	lc := net.ListenConfig{}
	tln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(eg.ProxyPort))
	if err != nil {
		return err
	}
	eln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(eg.ExplicitPort))
	if err != nil {
		return err
	}
	dln, err := lc.Listen(ctx, "tcp", ":"+strconv.Itoa(eg.DNSPort))
	if err != nil {
		return err
	}
	dpc, err := lc.ListenPacket(ctx, "udp", ":"+strconv.Itoa(eg.DNSPort))
	if err != nil {
		return err
	}
	go func() { _ = proxy.ServeTransparent(tln) }()
	go func() {
		_ = (&http.Server{Handler: proxy.ExplicitHandler(), ReadHeaderTimeout: 30 * time.Second}).Serve(eln)
	}()
	go func() { _ = dns.ServeTCP(dln) }()
	go func() { _ = dns.ServeUDP(dpc) }()

	if cfg.Sandbox.Driver != "docker" {
		logger.Info("egress: proxy listening; no firewall (not using the docker driver)")
		return nil
	}
	fw := egress.Firewall{
		Pool:      netip.MustParsePrefix(cfg.Sandbox.Docker.NetworkPool),
		ProxyPort: eg.ProxyPort, DNSPort: eg.DNSPort,
		OpenPorts: []int{tunnelPort, eg.ExplicitPort},
	}
	if err := fw.Install(); err != nil {
		return err
	}
	logger.Info("egress: firewall installed", "pool", fw.Pool, "dns_upstream", upstream)
	return nil
}

func newDriver(ctx context.Context, cfg *config.Config, egressOn bool, logger *slog.Logger) (sandbox.Driver, error) {
	switch cfg.Sandbox.Driver {
	case "fake":
		logger.Warn("using the fake sandbox driver: terminals run on this machine as the server's user")
		fakeharness.Command = filepath.Join(filepath.Dir(cfg.Sandbox.SandboxdPath), "fakeagent")
		d := fake.New(cfg.Sandbox.FakeDir)
		d.RunSandboxd("127.0.0.1", logger)
		return d, nil
	default:
		dc := cfg.Sandbox.Docker
		dcfg := docker.Config{
			Host: dc.Host, NamePrefix: dc.NamePrefix, NetworkPool: netip.MustParsePrefix(dc.NetworkPool),
			SubnetBits: dc.SubnetBits, Runtime: dc.Runtime, ServerContainer: dc.ServerContainer,
			PIDsLimit: cfg.Sandbox.PIDsLimit, TunnelHost: dc.TunnelHost, Logger: logger,
			Egress: egressOn && dc.TunnelHost == "",
		}
		if dc.SandboxdVolumeDir != "" {
			if err := installSandboxd(cfg.Sandbox.SandboxdPath, dc.SandboxdVolumeDir); err != nil {
				return nil, err
			}
			dcfg.SandboxdSource = dc.SandboxdVolume
		} else {
			dcfg.SandboxdSource = filepath.Dir(cfg.Sandbox.SandboxdPath)
		}
		return docker.New(ctx, dcfg)
	}
}

// installSandboxd copies sandboxd (and the scripted test agent, if it was
// built) into the volume shared with sandboxes.
func installSandboxd(src, dir string) error {
	if err := installBinary(src, filepath.Join(dir, "sandboxd")); err != nil {
		return fmt.Errorf("install sandboxd (sandbox.sandboxd_path): %w", err)
	}
	fa := filepath.Join(filepath.Dir(src), "fakeagent")
	if _, err := os.Stat(fa); err == nil {
		return installBinary(fa, filepath.Join(dir, "fakeagent"))
	}
	return nil
}

func installBinary(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
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
