package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/egress"
	fakeharness "github.com/gempir/agenthold/internal/harness/fake"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/runtimeassets"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/sandbox/firecracker"
	"github.com/gempir/agenthold/internal/tunnel"
)

// localHost is the part of agenthold that runs VMs on this machine: driver,
// guest firewall, tunnel endpoint and egress proxy. The all-in-one server and
// `agenthold node` both run one.
type localHost struct {
	node   *node.Local
	driver string
	cfg    *config.Config
	log    *slog.Logger

	mu sync.Mutex
	ca *egress.CA
}

// startLocalHost prepares this host to run sandboxes with driverName
// ("firecracker" or "fake"). controller is the controller an edge node joined (zero for the
// all-in-one server): a missing base image is fetched from it. Listeners stop when ctx ends.
func startLocalHost(ctx context.Context, cfg *config.Config, driverName string, controller runtimeassets.Controller, logger *slog.Logger) (*localHost, error) {
	if driverName == "firecracker" {
		if err := firecracker.CheckHost(ctx); err != nil {
			return nil, err
		}
		paths, err := runtimeassets.Prepare(ctx, cfg.DataDir, runtimeassets.Options{
			BaseURL: cfg.Sandbox.Firecracker.BaseURL, Controller: controller, Log: logger,
		})
		if err != nil {
			return nil, fmt.Errorf("runtime artifacts: %w", err)
		}
		if paths.Bundled {
			cfg.Sandbox.Firecracker.Binary = paths.Firecracker
			cfg.Sandbox.Firecracker.Jailer = paths.Jailer
			cfg.Sandbox.Firecracker.Kernel = paths.Kernel
			cfg.Sandbox.Firecracker.BaseImage = paths.BaseImage
			cfg.Sandbox.SandboxdPath = paths.Sandboxd
			logger.Info("machines boot this binary as their sandboxd", "path", paths.Sandboxd)
			logger.Info("release runtime ready", "version", paths.Version, "base", paths.BaseImage)
		} else {
			base, err := runtimeassets.PinBase(ctx, cfg.DataDir, cfg.Sandbox.Firecracker.BaseImage)
			if err != nil {
				return nil, fmt.Errorf("pin development base image: %w", err)
			}
			cfg.Sandbox.Firecracker.BaseImage = base
		}
	}
	tunnelPort, err := portOf(cfg.Sandbox.TunnelListen)
	if err != nil {
		return nil, fmt.Errorf("sandbox.tunnel_listen: %w", err)
	}
	fw := &egress.Firewall{
		Pool:      netip.MustParsePrefix(cfg.Sandbox.NetworkPool),
		ProxyPort: cfg.Egress.ProxyPort, DNSPort: cfg.Egress.DNSPort,
		OpenPorts: []int{tunnelPort, cfg.Egress.ExplicitPort},
	}
	h := &localHost{driver: driverName, cfg: cfg, log: logger}
	driver, err := newDriver(ctx, cfg, driverName, fw, driverCallbacks{
		shutdown: func(ctx context.Context, id sandbox.ID) error { return node.Client(h.node, string(id)).Shutdown(ctx) },
		suspend:  func(ctx context.Context, id sandbox.ID) error { return node.Client(h.node, string(id)).Suspend(ctx) },
		connected: func(ctx context.Context, id sandbox.ID) error {
			return h.node.WaitConnected(ctx, string(id))
		},
	}, logger)
	if err != nil {
		return nil, err
	}
	hub := tunnel.NewHub(func(ctx context.Context, agentID, token string) error {
		return h.node.AuthenticateTunnel(ctx, agentID, token)
	}, logger)
	h.node = node.NewLocal(driver, hub)
	h.node.SetDataDir(cfg.DataDir)
	h.node.SetTunnelPort(tunnelPort)
	if driverName == "firecracker" {
		if err := fw.Install(); err != nil {
			return nil, fmt.Errorf("guest firewall: %w", err)
		}
	}
	tunnelSrv := &http.Server{Handler: tunnelMux(hub), ReadHeaderTimeout: 10 * time.Second}
	tunnelLn, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.Sandbox.TunnelListen)
	if err != nil {
		return nil, fmt.Errorf("tunnel listener: %w", err)
	}
	go func() {
		if err := tunnelSrv.Serve(tunnelLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("tunnel listener", "err", err)
		}
	}()
	go func() { <-ctx.Done(); _ = tunnelSrv.Close() }()
	return h, nil
}

// useCA installs the egress CA and starts the proxy the first time. An edge
// node receives the controller's CA after every reconnect; it cannot change
// while the proxy runs.
func (h *localHost) useCA(ctx context.Context, ca *egress.CA) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ca != nil {
		if !bytes.Equal(h.ca.CertPEM(), ca.CertPEM()) {
			return errors.New("the controller's egress CA changed; restart this node")
		}
		return nil
	}
	h.node.SetCA(ca)
	if !h.cfg.Egress.Disabled {
		if err := startEgress(ctx, h.cfg, ca, h.node.ProxyAgents(), h.log); err != nil {
			return fmt.Errorf("egress proxy: %w", err)
		}
	}
	h.ca = ca
	return nil
}

// driverCallbacks reach a machine's sandboxd through this node's tunnel hub.
type driverCallbacks struct {
	shutdown, suspend, connected func(context.Context, sandbox.ID) error
}

func newDriver(ctx context.Context, cfg *config.Config, name string, fw *egress.Firewall, cb driverCallbacks, logger *slog.Logger) (sandbox.Driver, error) {
	switch name {
	case "fake":
		logger.Warn("using the fake sandbox driver: terminals run on this machine as the server's user")
		fakeharness.SetCommand(filepath.Join(filepath.Dir(cfg.Sandbox.SandboxdPath), "fakeagent"))
		d := fake.New(cfg.Sandbox.FakeDir)
		d.RunSandboxd("127.0.0.1", logger)
		return d, nil
	case "firecracker":
		fc := cfg.Sandbox.Firecracker
		for _, p := range []*string{&fc.Binary, &fc.Jailer, &fc.Kernel, &fc.BaseImage} {
			abs, err := filepath.Abs(*p)
			if err != nil {
				return nil, err
			}
			*p = abs
		}
		dataDir, err := filepath.Abs(cfg.DataDir)
		if err != nil {
			return nil, err
		}
		if fc.TransparentHugepages != "keep" {
			if prev, err := firecracker.EnableHugePages(); err != nil {
				logger.Warn("guest memory is faulted in 4 KiB pages: VMs boot and run slower", "err", err)
			} else if prev != "always" {
				logger.Info("switched transparent huge pages to always for guest memory", "was", prev)
			}
		}
		nested := firecracker.NestedVirtualization()
		snapshots := fc.SnapshotsEnabled(nested)
		if nested != "" && fc.Snapshots == nil {
			logger.Info("memory snapshots are off: this host is a virtual machine, where resumed guests run far slower than cold-booted ones; set sandbox.firecracker.snapshots to override", "hypervisor", nested)
		}
		return firecracker.New(ctx, firecracker.Config{
			DataDir: dataDir, BaseImagePath: fc.BaseImage, KernelPath: fc.Kernel,
			FirecrackerPath: fc.Binary, JailerPath: fc.Jailer, SandboxdPath: cfg.Sandbox.SandboxdPath,
			NetworkPool:      netip.MustParsePrefix(cfg.Sandbox.NetworkPool),
			OverlaySizeBytes: fc.OverlayGB << 30, HomeSizeBytes: fc.HomeGB << 30,
			Shutdown: cb.shutdown, RegisterNetwork: fw.Register, UnregisterNetwork: fw.Unregister,
			Snapshots: snapshots, Suspend: cb.suspend, WaitConnected: cb.connected,
			SnapshotMaxBytes: fc.SnapshotMaxGB << 30, SnapshotReserveBytes: fc.SnapshotReserveGB << 30,
			Balloon: fc.BalloonEnabled(), Log: logger,
		})
	default:
		return nil, fmt.Errorf("unknown sandbox driver %q", name)
	}
}
