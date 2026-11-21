package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/gempir/agenthold/internal/sandboxd"
	"github.com/gempir/agenthold/internal/version"
)

// The agenthold binary is also the daemon inside every agent machine (sandboxd): the node
// delivers its own executable to each guest as the initramfs's /init, and the running guest sees
// it at /opt/agenthold/sandboxd. A release therefore ships one binary per architecture.

type role int

const (
	roleServer   role = iota // the server, an edge node or a host subcommand
	roleInit                 // PID 1 of a guest: assemble the machine, then run the daemon as a child
	roleSandboxd             // the in-guest daemon and its helpers (mcp, net-setup)
)

// roleOf decides once, from how the process was started (never from configuration or the
// environment), what this process is, and returns the arguments left for that role. A guest's
// init is PID 1 started as /init (the initramfs) or as sandboxd. Every in-guest command line names
// the binary sandboxd (/opt/agenthold/sandboxd mcp, init's daemon child). "agenthold sandboxd ..."
// runs the daemon explicitly (development and tests). Anything else, including agenthold as PID 1
// of a container, is the server.
func roleOf(pid int, args []string) (role, []string) {
	if len(args) == 0 {
		return roleServer, nil
	}
	name := filepath.Base(args[0])
	switch {
	case pid == 1 && (name == "init" || name == "sandboxd"):
		return roleInit, nil
	case name == "sandboxd":
		return roleSandboxd, args[1:]
	case len(args) > 1 && args[1] == "sandboxd":
		return roleSandboxd, args[2:]
	}
	return roleServer, args[1:]
}

// runInit becomes the guest's init and never returns.
func runInit() {
	self, err := os.Executable()
	if err != nil {
		self = os.Args[0]
	}
	sandboxd.RunInit(self)
}

// runSandboxd is the in-guest daemon: it dials the node and serves terminals and files. Started by
// init, a shutdown request powers the VM off; otherwise (tests, development) it only ends the
// daemon.
func runSandboxd(args []string) error {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	if len(args) > 0 && args[0] == "mcp" {
		return sandboxd.ServeMCP(os.Stdin, os.Stdout)
	}
	if len(args) > 0 && args[0] == "net-setup" {
		fs := flag.NewFlagSet("net-setup", flag.ExitOnError)
		gw := fs.String("gateway", "", "address of the server on the agent network")
		_ = fs.Parse(args[1:])
		return sandboxd.NetSetup(*gw)
	}
	fs := flag.NewFlagSet("sandboxd", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	bootPath := fs.String("boot", envOr("AGENTHOLD_BOOT", sandboxd.BootFile), "boot configuration written by the server")
	_ = fs.Parse(args)
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
		cfg.BootPath = *bootPath
		cfg.ResumePort = sandboxd.ResumePort
		cfg.OnShutdown = func() {
			if err := sandboxd.RequestShutdown(); err != nil {
				logger.Error("shutdown", "err", err)
			}
		}
	} else {
		// A test or development run: a shutdown request ends the daemon.
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
