package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/runtimeassets"
	"github.com/gempir/agenthold/internal/sandbox/firecracker"
	"github.com/gempir/agenthold/internal/version"
)

// runNode runs this host as an edge node of a controller: it dials the
// controller (and nothing else but the internet for its agents), serves the
// node protocol over that one connection and reconnects with backoff.
func runNode(args []string) error {
	fs := flag.NewFlagSet("node", flag.ContinueOnError)
	join := fs.String("join", "", "join token from the controller (Settings → Nodes); only needed once")
	name := fs.String("name", "", "node name proposed when joining (default: node.name or the hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	slog.SetDefault(logger)
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if *name != "" {
		cfg.Node.Name = *name
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	state, err := enrollment.LoadState(cfg.Node.StateFile)
	var token enrollment.Token
	switch {
	case err == nil:
		if *join != "" {
			logger.Info("already joined; ignoring --join (delete the state file to join again)", "state", cfg.Node.StateFile, "node", state.NodeID)
		}
	case errors.Is(err, os.ErrNotExist):
		if *join == "" {
			return errors.New("this node has not joined a controller yet: create a join token in the web UI (Settings → Nodes) and run agenthold node --join <token>")
		}
		if token, err = enrollment.ParseToken(*join); err != nil {
			return err
		}
	default:
		return err
	}

	driverName := cfg.Sandbox.LocalDriver(firecracker.KVMPresent())
	if driverName == "none" {
		return errors.New("this host cannot run VMs (no /dev/kvm); an edge node needs Linux with KVM")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return err
	}
	if driverName == "firecracker" {
		// Before the token is spent: a host that cannot run VMs should not burn it.
		if err := firecracker.CheckHost(ctx); err != nil {
			return err
		}
	}
	var conn net.Conn
	var controller runtimeassets.Controller
	if state == nil {
		conn, state, err = enrollment.Join(ctx, cfg.Node.StateFile, token, cfg.Node.Name)
		if err != nil {
			if enrollment.IsDenied(err) {
				return fmt.Errorf("join refused: the token is invalid, expired or already used: %w", err)
			}
			return fmt.Errorf("join %s: %w", token.Endpoint, err)
		}
		logger.Info("joined controller", "node", state.NodeID, "name", state.Name, "controller", state.Endpoint)
		// Fetching the base image can take long: do not hold an idle connection meanwhile. The
		// controller sees a node that connects and goes away before it answered its setup call;
		// that changes no node state (cluster.Serve returns before registering it).
		_ = conn.Close()
		conn = nil
		controller = runtimeassets.Controller{DownloadURL: state.DownloadURL, Version: state.ControllerVersion, Live: true}
	} else {
		// What the state file says may be stale (the controller was upgraded since). It is only
		// worth asking again when the base image has to be fetched, which Prepare decides.
		controller = runtimeassets.Controller{
			DownloadURL: state.DownloadURL, Version: state.ControllerVersion,
			Refresh: func(ctx context.Context) (runtimeassets.Controller, error) {
				return askController(ctx, cfg.Node.StateFile, state, logger)
			},
		}
	}
	host, err := startLocalHost(ctx, cfg, driverName, controller, logger)
	if err != nil {
		return err
	}
	opts := node.ServeOptions{SetEgressCA: func(certPEM, keyPEM []byte) error {
		ca, err := egress.ParseCA(certPEM, keyPEM)
		if err != nil {
			return err
		}
		return host.useCA(ctx, ca)
	}}
	logger.Info("edge node starting", "version", version.Version, "driver", driverName, "controller", state.Endpoint)
	backoff := time.Second
	for {
		before := *state
		if conn == nil {
			conn, err = enrollment.Connect(ctx, state)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				if enrollment.IsDenied(err) {
					return fmt.Errorf("the controller refused this node (was it removed?): delete %s and join again with a new token: %w", cfg.Node.StateFile, err)
				}
				wait := jitter(backoff)
				logger.Warn("controller unreachable, retrying", "controller", state.Endpoint, "in", wait.String(), "err", err)
				if !sleepCtx(ctx, wait) {
					return nil
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
		}
		backoff = time.Second
		saveController(cfg.Node.StateFile, state, before, logger)
		logger.Info("connected to controller", "controller", state.Endpoint)
		err = node.ServeRemote(ctx, conn, host.node, opts)
		conn = nil
		if ctx.Err() != nil {
			return nil
		}
		logger.Warn("controller connection lost; agents keep running", "err", err)
		if !sleepCtx(ctx, jitter(backoff)) {
			return nil
		}
	}
}

// askController asks the controller which release it runs and where it serves downloads from,
// with a connection that is closed right away (see the note at the join). It saves what it learned.
func askController(ctx context.Context, path string, state *enrollment.State, logger *slog.Logger) (runtimeassets.Controller, error) {
	before := *state
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, err := enrollment.Connect(ctx, state)
	if err != nil {
		return runtimeassets.Controller{}, err
	}
	_ = conn.Close()
	saveController(path, state, before, logger)
	return runtimeassets.Controller{DownloadURL: state.DownloadURL, Version: state.ControllerVersion, Live: true}, nil
}

// saveController persists what the controller said about itself when it changed.
func saveController(path string, state *enrollment.State, before enrollment.State, logger *slog.Logger) {
	if state.DownloadURL == before.DownloadURL && state.ControllerVersion == before.ControllerVersion {
		return
	}
	if err := state.Save(path); err != nil {
		logger.Warn("save node state", "err", err)
	}
}

func jitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d) //nolint:gosec // not security relevant
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
