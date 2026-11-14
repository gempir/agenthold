// Package sandbox defines the driver interface that isolates agents and the
// manager that drives each agent's sandbox through its lifecycle.
//
// Nothing outside the driver packages may import a container runtime SDK.
package sandbox

import (
	"context"
	"errors"
	"net/netip"
)

// ID identifies a sandbox inside its driver. It is stable for the life of the
// sandbox, including rebuilds.
type ID string

// Resources limits a sandbox. Zero means "driver default / unlimited".
type Resources struct {
	CPUs      float64 // fractional cores
	MemoryMB  int64
	DiskGB    int64 // best effort; not every driver can enforce it
	PIDsLimit int64
}

// Mount is an extra read-only bind mount (for example sandboxd).
type Mount struct {
	Source, Target string
	ReadOnly       bool
}

// Spec describes a sandbox.
type Spec struct {
	// AgentID is the platform's agent id. Drivers use it to name and label resources.
	AgentID   string
	Image     string
	Resources Resources
	// Env is the environment of the sandbox's entrypoint.
	Env map[string]string
	// Entrypoint and Cmd override the image's. Empty means image default.
	Entrypoint []string
	Cmd        []string
	Mounts     []Mount
	// HomeVolume is the name of the persistent volume mounted at /home/agent.
	// Empty means the driver derives it from AgentID.
	HomeVolume string
}

// State is what a driver reports about a sandbox.
type State string

// Driver-level states.
const (
	StateMissing State = "missing" // the driver does not know this sandbox
	StateCreated State = "created" // exists, not running
	StateRunning State = "running"
	StateStopped State = "stopped" // exited or stopped; disk kept
)

// Status is the driver's view of a sandbox.
type Status struct {
	State State
	// Detail is a human-readable hint, for example an exit code.
	Detail string
	// GatewayIP is the address the sandbox reaches the server at, if known.
	GatewayIP string
	// Subnet is the sandbox's network, used to attribute its traffic.
	Subnet netip.Prefix
}

// Boot is the per-boot configuration for sandboxd. The driver knows how the
// sandbox reaches the server and turns TunnelPort into a URL.
type Boot struct {
	AgentID    string
	Token      string
	TunnelPort int
	// Files are written into the sandbox before it starts (path -> content),
	// for example the egress CA certificate.
	Files map[string][]byte
}

// ServerPlaceholder in Spec.Env values is replaced by the address sandboxes
// use to reach the server.
const ServerPlaceholder = "{{server}}"

// SandboxdDir is where drivers mount the sandboxd binary inside a sandbox.
const SandboxdDir = "/opt/agenthold"

// DestroyOpts controls what Destroy removes.
type DestroyOpts struct {
	KeepHome bool
}

// Driver isolates sandboxes. Implementations must be safe for concurrent use
// and idempotent where noted.
type Driver interface {
	// Name is the driver's name, e.g. "docker".
	Name() string
	// Create provisions a sandbox (network, volume, container) without starting it.
	Create(ctx context.Context, spec Spec) (ID, error)
	// Start starts a created or stopped sandbox with a fresh boot configuration
	// (written to sandboxd's boot file before the entrypoint runs). Starting a
	// running sandbox is a no-op.
	Start(ctx context.Context, id ID, boot Boot) error
	// Stop stops a sandbox and keeps its disk ("sleep"). Stopping a stopped sandbox is a no-op.
	Stop(ctx context.Context, id ID) error
	// Destroy removes the sandbox. Destroying a missing sandbox is a no-op.
	Destroy(ctx context.Context, id ID, opts DestroyOpts) error
	// Status reports the sandbox's state. Unknown ids report StateMissing, not an error.
	Status(ctx context.Context, id ID) (Status, error)
	// Rebuild replaces the sandbox with one built from spec (new image), keeping
	// the id and the home volume. The sandbox ends up stopped.
	Rebuild(ctx context.Context, id ID, spec Spec) error
}

// Attacher is implemented by drivers that must reconnect the server to a
// running sandbox after the server itself was restarted or recreated.
type Attacher interface {
	Attach(ctx context.Context, id ID) error
}

// ErrNotFound is returned by Start and Stop for unknown sandboxes.
var ErrNotFound = errors.New("sandbox not found")
