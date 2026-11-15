// Package firecracker runs persistent microVMs through Firecracker's jailer.
// The caller owns host egress policy; RegisterNetwork must install that policy
// before a VM is started. Guest disks and manifests are kept below DataDir.
package firecracker

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
)

// ErrUnsupported is returned on hosts which cannot run Firecracker.
var ErrUnsupported = errors.New("firecracker requires Linux, root, and /dev/kvm")

// Config contains extracted runtime paths, independent of application config.
// The base image must contain sandboxd at /opt/agenthold/sandboxd (the N2 image
// contract). Updating a host sandboxd binary alone does not update a guest image.
// Runtime files and DataDir must be in trusted, administrator-owned directories.
// Callbacks must honor their contexts.
type Config struct {
	DataDir         string
	BaseImagePath   string
	KernelPath      string
	FirecrackerPath string
	JailerPath      string
	// JailDir defaults to DataDir/jailer. Keep it short: Linux Unix socket paths
	// are limited to 107 bytes. All configured paths must be absolute.
	JailDir     string
	NetworkPool netip.Prefix
	// Each allocated /30 gets a distinct uid/gid: base + its pool index.
	// Both default to 100000; zero/root is never used for the VMM.
	UIDBase uint32
	GIDBase uint32
	// Default writable capacities. Blank sparse disks are formatted as ext4 by
	// sandboxd at first boot. Spec.Resources.DiskGB overrides their combined size.
	OverlaySizeBytes int64
	HomeSizeBytes    int64
	// MemoryOverheadMB is host cgroup allowance in addition to guest RAM (128).
	MemoryOverheadMB int64
	StopTimeout      time.Duration
	StartTimeout     time.Duration
	// Shutdown asks sandboxd to power off through its tunnel. On failure, or if
	// absent, the driver tries SendCtrlAltDel before the timeout/kill fallback.
	Shutdown func(context.Context, sandbox.ID) error
	// RegisterNetwork installs per-tap host policy and traffic attribution. Both
	// callbacks are required by New. Registration must fail if host policy is
	// not installed. Registration must be idempotent. UnregisterNetwork is called before deleting the tap,
	// including after a partial registration failure; it must be idempotent too.
	// Attach re-registers running VMs after a controller/node restart.
	RegisterNetwork   func(context.Context, string, netip.Addr) error
	UnregisterNetwork func(context.Context, string, netip.Addr) error
}

func (c Config) normalized() (Config, error) {
	for name, p := range map[string]string{"data directory": c.DataDir, "base image": c.BaseImagePath, "kernel": c.KernelPath, "firecracker": c.FirecrackerPath, "jailer": c.JailerPath} {
		if p == "" || !filepath.IsAbs(p) {
			return c, fmt.Errorf("%s path must be absolute", name)
		}
	}
	if c.JailDir == "" {
		c.JailDir = filepath.Join(c.DataDir, "jailer")
	}
	if !filepath.IsAbs(c.JailDir) {
		return c, errors.New("jail directory path must be absolute")
	}
	if !c.NetworkPool.IsValid() || !c.NetworkPool.Addr().Is4() || c.NetworkPool.Bits() < 8 || c.NetworkPool.Bits() > 30 {
		return c, errors.New("network pool must be an IPv4 prefix between /8 and /30")
	}
	c.NetworkPool = c.NetworkPool.Masked()
	if c.UIDBase == 0 {
		c.UIDBase = 100000
	}
	if c.GIDBase == 0 {
		c.GIDBase = 100000
	}
	count := uint64(1) << (30 - c.NetworkPool.Bits())
	if uint64(c.UIDBase)+count > math.MaxUint32 || uint64(c.GIDBase)+count > math.MaxUint32 {
		return c, errors.New("jailer uid/gid range overflows")
	}
	if c.OverlaySizeBytes == 0 {
		c.OverlaySizeBytes = 4 << 30
	}
	if c.HomeSizeBytes == 0 {
		c.HomeSizeBytes = 8 << 30
	}
	for _, n := range []int64{c.OverlaySizeBytes, c.HomeSizeBytes} {
		if n < 64<<20 || n%4096 != 0 {
			return c, errors.New("disk sizes must be at least 64 MiB and multiples of 4096")
		}
	}
	if c.MemoryOverheadMB == 0 {
		c.MemoryOverheadMB = 128
	}
	if c.MemoryOverheadMB < 0 || c.MemoryOverheadMB > 1<<20 {
		return c, errors.New("memory overhead must be between 0 and 1048576 MiB")
	}
	if c.StopTimeout == 0 {
		c.StopTimeout = 10 * time.Second
	}
	if c.StartTimeout == 0 {
		c.StartTimeout = 10 * time.Second
	}
	if c.StopTimeout < 0 || c.StartTimeout < 0 {
		return c, errors.New("timeouts must be positive")
	}
	if (c.RegisterNetwork == nil) != (c.UnregisterNetwork == nil) {
		return c, errors.New("network register and unregister callbacks must be supplied together")
	}
	return c, nil
}

func validID(id string) bool {
	if id == "" || len(id) > 128 || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func validateSpec(s sandbox.Spec) error {
	if !validID(s.AgentID) {
		return errors.New("agent id must contain only alphanumeric characters, underscores and hyphens")
	}
	r := s.Resources
	if math.IsNaN(r.CPUs) || math.IsInf(r.CPUs, 0) || r.CPUs < 0 || r.CPUs > 32 || r.CPUs > 0 && r.CPUs < 0.01 {
		return errors.New("CPU limit must be zero (default) or between 0.01 and 32")
	}
	if r.MemoryMB < 0 || r.MemoryMB > 1<<20 {
		return errors.New("memory limit must be between 0 and 1048576 MiB")
	}
	if r.DiskGB < 0 || r.DiskGB > 1<<20 {
		return errors.New("disk limit must be between 0 and 1048576 GiB")
	}
	for k, v := range s.Env {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return errors.New("invalid guest environment")
		}
	}
	return nil
}

func capacities(c Config, s sandbox.Spec) (overlay, home int64) {
	overlay, home = c.OverlaySizeBytes, c.HomeSizeBytes
	if s.Resources.DiskGB > 0 {
		total := s.Resources.DiskGB << 30
		overlay = min(overlay, total/2)
		home = total - overlay
	}
	return
}
