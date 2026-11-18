// Package sandbox defines the driver interface that isolates agents and the
// manager that drives each agent's sandbox through its lifecycle.
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
	CPUs     float64 // fractional cores
	MemoryMB int64
	DiskGB   int64 // best effort; not every driver can enforce it
}

// Spec describes a sandbox.
type Spec struct {
	// AgentID is the platform's agent id. Drivers use it to name and label resources.
	AgentID string
	// Image is the internal base revision used when comparing applied specs.
	Image     string
	Resources Resources
	// Env is added to processes started by sandboxd.
	Env map[string]string
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
	// BaseStale is only ever set by an edge node running a release before G1
	// (host-delivered sandboxd): its system disk sits on an older base than
	// the node's current one, so its guest boots the older sandboxd baked
	// into that base. No driver of this release sets it.
	//
	// TODO(G1+1): remove BaseStale and ErrBaseStale one release after G1,
	// once no supported node can run a release before G1 (edge nodes run
	// their controller's release, see the version rules of D1a).
	BaseStale bool
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

// SandboxdDir is where the guest base image contains the sandboxd binary.
const SandboxdDir = "/opt/agenthold"

// DestroyOpts controls what Destroy removes.
type DestroyOpts struct {
	KeepHome bool
}

// Driver isolates sandboxes. Implementations must be safe for concurrent use
// and idempotent where noted.
type Driver interface {
	// Name is the driver's name, e.g. "firecracker".
	Name() string
	// Create provisions persistent disks and a private network without starting the guest.
	Create(ctx context.Context, spec Spec) (ID, error)
	// Start starts a created or stopped sandbox with a fresh boot configuration
	// (delivered through a read-only config disk before sandboxd runs). Starting a
	// running sandbox is a no-op.
	Start(ctx context.Context, id ID, boot Boot) error
	// Stop stops a sandbox and keeps its disk ("sleep"). Stopping a stopped sandbox is a no-op.
	Stop(ctx context.Context, id ID) error
	// Destroy removes the sandbox. Destroying a missing sandbox is a no-op.
	Destroy(ctx context.Context, id ID, opts DestroyOpts) error
	// Status reports the sandbox's state. Unknown ids report StateMissing, not an error.
	Status(ctx context.Context, id ID) (Status, error)
	// Rebuild resets the system disk using spec and the current base image,
	// keeping the id and home disk. The sandbox ends up stopped.
	Rebuild(ctx context.Context, id ID, spec Spec) error
	// Clone provisions a new, stopped sandbox for spec.AgentID whose system
	// and home disks are copies of src's. src must not be running (the manager
	// guarantees it; drivers refuse with ErrRunning). The clone has its own
	// network allocation and boots as its own machine. A clone that fails or is
	// cancelled leaves nothing behind. Cloning onto an existing sandbox of
	// spec.AgentID returns it unchanged.
	Clone(ctx context.Context, src ID, spec Spec) (ID, error)
}

// Attacher is implemented by drivers that must reconnect the server to a
// running sandbox after the server itself was restarted or recreated.
type Attacher interface {
	Attach(ctx context.Context, id ID) error
}

// ErrNotFound is returned by Start and Stop for unknown sandboxes.
var ErrNotFound = errors.New("sandbox not found")

// ErrRunning is returned by Clone when the source sandbox is running.
var ErrRunning = errors.New("sandbox is running")

// ErrUnreachable is returned when the node that hosts a sandbox is not
// connected. The sandbox's persisted state is kept until the node returns.
var ErrUnreachable = errors.New("node is unreachable")

// ErrNoCapacity is returned when no connected node can take a new agent.
var ErrNoCapacity = errors.New("no node is available to run agents")

// Placement is where a new agent's sandbox lives for its whole life.
type Placement struct {
	NodeID string
	Driver string
}

// Placer is implemented by backends that span several nodes.
type Placer interface {
	Place(ctx context.Context) (Placement, error)
}

// Reachability is implemented by backends whose nodes can be disconnected.
// Reachable returns an error wrapping ErrUnreachable when the agent's node is
// not connected.
type Reachability interface {
	Reachable(ctx context.Context, agentID string) error
}
