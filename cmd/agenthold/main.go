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
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/docker"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/server"
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
	driver, err := newDriver(ctx, cfg, logger)
	if err != nil {
		return err
	}
	var sandboxes *sandbox.Manager
	hub := tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return sandboxes.AuthenticateTunnel(ctx, agentID, token)
	}, logger)
	sandboxes = sandbox.NewManagerWithOptions(st, driver, sandbox.BaseSpec, sandbox.Options{Tunnel: hub, TunnelPort: tunnelPort}, logger)
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

	apiSrv := api.New(api.Deps{Store: st, Auth: authSvc, PublicURL: cfg.PublicURL, Sandboxes: sandboxes, Tunnel: hub, Limits: cfg.Sandbox})

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

func newDriver(ctx context.Context, cfg *config.Config, logger *slog.Logger) (sandbox.Driver, error) {
	switch cfg.Sandbox.Driver {
	case "fake":
		logger.Warn("using the fake sandbox driver: terminals run on this machine as the server's user")
		d := fake.New(cfg.Sandbox.FakeDir)
		d.RunSandboxd("127.0.0.1", logger)
		return d, nil
	default:
		dc := cfg.Sandbox.Docker
		dcfg := docker.Config{
			Host: dc.Host, NamePrefix: dc.NamePrefix, NetworkPool: netip.MustParsePrefix(dc.NetworkPool),
			SubnetBits: dc.SubnetBits, Runtime: dc.Runtime, ServerContainer: dc.ServerContainer,
			PIDsLimit: cfg.Sandbox.PIDsLimit, TunnelHost: dc.TunnelHost, Logger: logger,
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

// installSandboxd copies the sandboxd binary into the volume shared with sandboxes.
func installSandboxd(src, dir string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read sandboxd (sandbox.sandboxd_path): %w", err)
	}
	dst := filepath.Join(dir, "sandboxd")
	if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, data) {
		return nil
	}
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, data, 0o755); err != nil {
		return fmt.Errorf("install sandboxd: %w", err)
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
