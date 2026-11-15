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
	host, err := startLocalHost(ctx, cfg, driverName, logger)
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

	var conn net.Conn
	if state == nil {
		conn, state, err = enrollment.Join(ctx, cfg.Node.StateFile, token, cfg.Node.Name)
		if err != nil {
			if enrollment.IsDenied(err) {
				return fmt.Errorf("join refused: the token is invalid, expired or already used: %w", err)
			}
			return fmt.Errorf("join %s: %w", token.Endpoint, err)
		}
		logger.Info("joined controller", "node", state.NodeID, "name", state.Name, "controller", state.Endpoint)
	}
	logger.Info("edge node starting", "version", version.Version, "driver", driverName, "controller", state.Endpoint)
	backoff := time.Second
	for {
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
