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
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/server"
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
	listen := flag.String("listen", envOr("AGENTHOLD_LISTEN", ":8080"), "HTTP listen address")
	devProxy := flag.String("dev-proxy", os.Getenv("AGENTHOLD_DEV_PROXY"), "proxy the web UI to this Vite dev server URL")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("agenthold %s (%s)\n", version.Version, version.Commit)
		return nil
	}

	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)

	cfg := server.Config{Web: web.Dist(), Logger: logger}
	if *devProxy != "" {
		u, err := url.Parse(*devProxy)
		if err != nil {
			return fmt.Errorf("parse dev proxy url: %w", err)
		}
		cfg.DevProxy = u
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           server.New(cfg),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() {
		logger.Info("listening", "addr", *listen, "version", version.Version, "dev_proxy", *devProxy)
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
