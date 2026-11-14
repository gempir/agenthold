// Command sandboxd is the daemon that runs inside every agent sandbox. It is
// the sandbox's entrypoint, dials the server and serves terminals and files.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/version"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("sandboxd", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	showVersion := flag.Bool("version", false, "print version and exit")
	bootPath := flag.String("boot", envOr("AGENTHOLD_BOOT", sandboxd.BootFile), "boot configuration written by the server")
	flag.Parse()
	if *showVersion {
		fmt.Printf("sandboxd %s (%s)\n", version.Version, version.Commit)
		return nil
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	boot, err := sandboxd.LoadBoot(*bootPath)
	if err != nil {
		return fmt.Errorf("no boot configuration; was this sandbox started by agenthold? %w", err)
	}
	d, err := sandboxd.New(sandboxd.Config{Boot: boot, Version: version.Version, Logger: logger})
	if err != nil {
		return err
	}
	logger.Info("sandboxd started", "version", version.Version, "agent", boot.AgentID)
	_ = d.Run(ctx)
	logger.Info("sandboxd stopping")
	return nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
