//go:build !linux

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/netip"

	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// BuildLayerOptions configures an ephemeral Firecracker microVM layer build.
type BuildLayerOptions struct {
	BuildID        string
	BootFiles      map[string][]byte
	Env            map[string]string
	CPUs           float64
	MemoryMB       int64
	DiskGB         int64
	TunnelPort     int
	SetToken       func(ctx context.Context, agentID, hash string) error
	WaitSession    func(ctx context.Context, agentID string) (*tunnel.Session, error)
	RegisterEgress func(agentID string, subnet netip.Prefix)
	RemoveEgress   func(agentID string)
	OnLog          func(line string)
}

// BuildLayerOutput holds the sealed layer produced by Driver.BuildLayer.
type BuildLayerOutput struct {
	Arch       string
	BaseKey    string
	RawPath    string
	RawSHA256  string
	RawSize    int64
	ZstdPath   string
	ZstdSHA256 string
	ZstdSize   int64
	Cleanup    func()
}

// Driver is unavailable on non-Linux hosts. Fake sandboxes remain portable.
type Driver struct{}

var (
	_ sandbox.Driver   = (*Driver)(nil)
	_ sandbox.Attacher = (*Driver)(nil)
)

// CheckHost reports that Firecracker is unavailable on this host.
func CheckHost(context.Context) error { return ErrUnsupported }

// KVMPresent is always false off Linux.
func KVMPresent() bool { return false }

// EnableHugePages reports that Firecracker is unavailable on this host.
func EnableHugePages() (string, error) { return "", ErrUnsupported }

// NestedVirtualization is always "" off Linux.
func NestedVirtualization() string { return "" }

// New reports that Firecracker is unavailable on this host.
func New(context.Context, Config) (*Driver, error) { return nil, ErrUnsupported }

// Name implements sandbox.Driver.
func (*Driver) Name() string { return "firecracker" }

// Create implements sandbox.Driver.
func (*Driver) Create(context.Context, sandbox.Spec) (sandbox.ID, error) { return "", ErrUnsupported }

// Start implements sandbox.Driver.
func (*Driver) Start(context.Context, sandbox.ID, sandbox.Boot) error { return ErrUnsupported }

// Stop implements sandbox.Driver.
func (*Driver) Stop(context.Context, sandbox.ID) error { return ErrUnsupported }

// Destroy implements sandbox.Driver.
func (*Driver) Destroy(context.Context, sandbox.ID, sandbox.DestroyOpts) error { return ErrUnsupported }

// Status implements sandbox.Driver.
func (*Driver) Status(context.Context, sandbox.ID) (sandbox.Status, error) {
	return sandbox.Status{}, ErrUnsupported
}

// Rebuild implements sandbox.Driver.
func (*Driver) Rebuild(context.Context, sandbox.ID, sandbox.Spec) error { return ErrUnsupported }

// Attach implements sandbox.Attacher.
func (*Driver) Attach(context.Context, sandbox.ID) error { return ErrUnsupported }

// TapName returns the stable host interface name for a sandbox.
func TapName(id sandbox.ID) string {
	s := sha256.Sum256([]byte(id))
	return "ah" + hex.EncodeToString(s[:6])
}

// Clone implements sandbox.Driver.
func (*Driver) Clone(context.Context, sandbox.ID, sandbox.Spec) (sandbox.ID, error) {
	return "", ErrUnsupported
}

// BuildLayer is only available on Linux hosts with KVM.
func (*Driver) BuildLayer(context.Context, BuildLayerOptions) (BuildLayerOutput, error) {
	return BuildLayerOutput{}, ErrUnsupported
}
