// Package firecracker runs persistent microVMs through Firecracker's jailer.
// The caller owns host egress policy; RegisterNetwork must install that policy
// before a VM is started. Guest disks and manifests are kept below DataDir.
package firecracker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"path/filepath"
	"strings"
	"time"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandboxd"
)

// LayerFile names one read-only ext4 file on the host stacked above the base
// image in a microVM's root overlayfs.
type LayerFile struct {
	Path string `json:"path"`
	// Subdir, when non-empty, names a relative directory inside the ext4 image
	// to use as the overlayfs lowerdir (for example "upper" for a sealed
	// overlay disk).
	Subdir string `json:"subdir,omitempty"`
}

// defaultCgroupRoot is where the jailer puts microVM cgroups (--parent-cgroup agenthold).
const defaultCgroupRoot = "/sys/fs/cgroup/agenthold"

// ErrUnsupported is returned on hosts which cannot run Firecracker.
var ErrUnsupported = errors.New("firecracker requires Linux, root, and /dev/kvm")

// Config contains extracted runtime paths, independent of application config.
// SandboxdPath is the host's sandboxd binary: every boot delivers it to the
// guest as an initramfs, so a guest always runs the sandboxd of the node that
// booted it, whatever base image its overlay sits on. Runtime files and DataDir must be in trusted, administrator-owned directories.
// Callbacks must honor their contexts.
type Config struct {
	DataDir         string
	BaseImagePath   string
	KernelPath      string
	FirecrackerPath string
	JailerPath      string
	SandboxdPath    string
	// DefaultLayers are node-wide read-only ext4 layers stacked below custom
	// Spec.Layers and above BaseImagePath (highest precedence first). They are
	// resolved dynamically at boot so platform layers (such as harnesses) can
	// update on restart without resetting a machine's overlay.
	DefaultLayers []LayerFile
	// ResolveLayer resolves each logical layer reference in Spec.Layers to a
	// trusted host LayerFile during Create and Rebuild.
	ResolveLayer func(ctx context.Context, ref string) (LayerFile, error)
	// JailDir defaults to DataDir/jailer. Keep it short: Linux Unix socket paths
	// are limited to 107 bytes. All configured paths must be absolute.
	JailDir string
	// CgroupRoot is the cgroup directory the jailer places VMs in (its
	// --parent-cgroup "agenthold" below /sys/fs/cgroup). It defaults to the real
	// path; tests that do not boot VMs point it at a temporary directory so they
	// never touch the hierarchy of a live service.
	CgroupRoot  string
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

	// Snapshots makes Stop keep the guest's memory: the VM is paused and a
	// full Firecracker snapshot is written next to its disks, and the next
	// Start resumes it (see snapshot_linux.go). Anything unexpected falls
	// back to a cold shutdown or a cold boot. It needs Suspend, which asks
	// sandboxd to flush its filesystems first (and fails for a sandboxd that
	// cannot resume), and WaitConnected, which bounds how long a resumed
	// guest may take to reconnect its tunnel before it is cold booted.
	Snapshots     bool
	Suspend       func(context.Context, sandbox.ID) error
	WaitConnected func(context.Context, sandbox.ID) error
	// SnapshotMaxBytes caps the disk space all snapshots of this node take
	// (0: no cap). A sleep that would exceed it shuts the VM down instead.
	SnapshotMaxBytes int64
	// SnapshotReserveBytes is the free space a snapshot must leave on the
	// data filesystem (default 2 GiB).
	SnapshotReserveBytes int64
	// Balloon adds a virtio balloon to every VM and inflates it before a
	// snapshot, so the guest's free memory is not written to disk.
	Balloon bool
	// SnapshotTimeout bounds writing a snapshot (default 2 min);
	// ResumeConnectTimeout bounds the tunnel reconnect after a resume
	// (default 15 s).
	SnapshotTimeout      time.Duration
	ResumeConnectTimeout time.Duration
	// Log receives snapshot fallbacks and measurements (default slog.Default()).
	Log *slog.Logger
}

func (c Config) normalized() (Config, error) {
	for name, p := range map[string]string{"data directory": c.DataDir, "base image": c.BaseImagePath, "kernel": c.KernelPath, "firecracker": c.FirecrackerPath, "jailer": c.JailerPath, "sandboxd": c.SandboxdPath} {
		if p == "" || !filepath.IsAbs(p) {
			return c, fmt.Errorf("%s path must be absolute", name)
		}
	}
	if len(c.DefaultLayers) > sandboxd.MaxGuestLayers {
		return c, fmt.Errorf("too many default layers (%d, max %d)", len(c.DefaultLayers), sandboxd.MaxGuestLayers)
	}
	for i, l := range c.DefaultLayers {
		if err := validateLayerFile(l); err != nil {
			return c, fmt.Errorf("default layer %d: %w", i, err)
		}
	}
	if c.JailDir == "" {
		c.JailDir = filepath.Join(c.DataDir, "jailer")
	}
	if c.CgroupRoot == "" {
		c.CgroupRoot = defaultCgroupRoot
	}
	if !filepath.IsAbs(c.CgroupRoot) {
		return c, errors.New("cgroup root path must be absolute")
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
	if c.SnapshotReserveBytes == 0 {
		c.SnapshotReserveBytes = 2 << 30
	}
	if c.SnapshotTimeout == 0 {
		c.SnapshotTimeout = 2 * time.Minute
	}
	if c.ResumeConnectTimeout == 0 {
		c.ResumeConnectTimeout = 15 * time.Second
	}
	if c.SnapshotMaxBytes < 0 || c.SnapshotReserveBytes < 0 || c.SnapshotTimeout < 0 || c.ResumeConnectTimeout < 0 {
		return c, errors.New("snapshot limits and timeouts must not be negative")
	}
	if c.Snapshots && (c.Suspend == nil || c.WaitConnected == nil) {
		return c, errors.New("snapshots need the suspend and wait-connected callbacks")
	}
	if c.Log == nil {
		c.Log = slog.Default()
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
	if len(s.Layers) > sandboxd.MaxGuestLayers {
		return fmt.Errorf("too many layers (%d, max %d)", len(s.Layers), sandboxd.MaxGuestLayers)
	}
	for i, ref := range s.Layers {
		if ref == "" || strings.ContainsAny(ref, "\x00 \t\r\n") {
			return fmt.Errorf("invalid layer reference at index %d", i)
		}
	}
	return nil
}

func validateLayerFile(l LayerFile) error {
	if l.Path == "" || !filepath.IsAbs(l.Path) {
		return errors.New("path must be absolute")
	}
	if err := sandboxd.ValidateLayerSubdir(l.Subdir); err != nil {
		return fmt.Errorf("subdir %q: %w", l.Subdir, err)
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
