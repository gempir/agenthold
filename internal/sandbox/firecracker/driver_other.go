//go:build !linux

package firecracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"github.com/gempir/agenthold/internal/sandbox"
)

// Driver is unavailable on non-Linux hosts. Fake sandboxes remain portable.
type Driver struct{}

var (
	_ sandbox.Driver   = (*Driver)(nil)
	_ sandbox.Attacher = (*Driver)(nil)
)

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
