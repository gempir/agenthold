// Command sandboxd is the daemon that runs inside every agent sandbox. It
// dials the server and serves terminals and files.
//
// Started as PID 1 (the kernel's init= in a microVM) it first acts as init:
// it assembles the root filesystem, network and boot configuration, then runs
// itself as the daemon child, reaps orphans and powers the VM off on request.
// Otherwise (a container entrypoint, tests) it is only the daemon.
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
	if os.Getpid() == 1 {
		self, err := os.Executable()
		if err != nil {
			self = os.Args[0]
		}
		sandboxd.RunInit(self) // never returns
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if err := run(logger); err != nil {
		logger.Error("sandboxd", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		return sandboxd.ServeMCP(os.Stdin, os.Stdout)
	}
	if len(os.Args) > 1 && os.Args[1] == "net-setup" {
		fs := flag.NewFlagSet("net-setup", flag.ExitOnError)
		gw := fs.String("gateway", "", "address of the server on the agent network")
		_ = fs.Parse(os.Args[2:])
		return sandboxd.NetSetup(*gw)
	}
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
	cfg := sandboxd.Config{Boot: boot, Version: version.Version, Logger: logger}
	if sandboxd.InInitChild() {
		// Started by init mode: a shutdown request powers the VM off, and
		// the host delivers the machine's services in the boot files.
		_ = os.Unsetenv(sandboxd.EnvInitChild)
		cfg.ServicesFile = sandboxd.ServicesFile
		cfg.OnShutdown = func() {
			if err := sandboxd.RequestShutdown(); err != nil {
				logger.Error("shutdown", "err", err)
			}
		}
	} else {
		// A container or test: a shutdown request ends the daemon.
		cfg.OnShutdown = stop
	}
	d, err := sandboxd.New(cfg)
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
