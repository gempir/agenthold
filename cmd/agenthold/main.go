// Command agenthold is the agenthold server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/auth"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/server"
	"github.com/gempir/agenthold/internal/store"
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
	apiSrv := api.New(api.Deps{Store: st, Auth: authSvc, PublicURL: cfg.PublicURL})

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
