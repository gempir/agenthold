// Package node defines the boundary between the controller and a sandbox host.
package node

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// Node operations use values and streams so they can be transported to a remote host.
type Node interface {
	Name(ctx context.Context) (string, error)
	Create(ctx context.Context, spec sandbox.Spec) (sandbox.ID, error)
	Start(ctx context.Context, id sandbox.ID, boot sandbox.Boot) error
	Stop(ctx context.Context, id sandbox.ID) error
	Destroy(ctx context.Context, id sandbox.ID, opts sandbox.DestroyOpts) error
	Status(ctx context.Context, id sandbox.ID) (sandbox.Status, error)
	Rebuild(ctx context.Context, id sandbox.ID, spec sandbox.Spec) error
	// Clone copies the stopped sandbox src into a new sandbox for spec.AgentID
	// on the same node.
	Clone(ctx context.Context, src sandbox.ID, spec sandbox.Spec) (sandbox.ID, error)
	Attach(ctx context.Context, id sandbox.ID) error
	WaitConnected(ctx context.Context, agentID string) error
	Connected(ctx context.Context, agentID string) (bool, error)
	Disconnect(ctx context.Context, agentID string) error
	OpenStream(ctx context.Context, agentID string, open tunnel.Open) (io.ReadWriteCloser, error)
	NextIncoming(ctx context.Context) (Incoming, io.ReadWriteCloser, error)
	SetAgentConfigs(context.Context, []AgentConfig) error
	NextIntercept(context.Context) (InterceptRequest, io.ReadWriteCloser, error)
	SetTunnelToken(context.Context, string, string) error
	CACertificate(context.Context) ([]byte, error)
	Capacity(context.Context) (Capacity, error)
}

// Incoming identifies a guest-originated stream received by the controller.
type Incoming struct {
	AgentID string
	Open    tunnel.Open
}
type incomingStream struct {
	info   Incoming
	stream io.ReadWriteCloser
	done   chan struct{}
}
type ownedStream struct {
	io.ReadWriteCloser
	done chan struct{}
	once sync.Once
}

func (s *ownedStream) Close() error {
	err := s.ReadWriteCloser.Close()
	s.once.Do(func() { close(s.done) })
	return err
}

// Local owns the in-process driver, tunnel endpoint and proxy state.
type Local struct {
	driver     sandbox.Driver
	hub        *tunnel.Hub
	incoming   chan incomingStream
	proxy      *proxyDirectory
	tokensMu   sync.RWMutex
	tokens     map[string]string
	caMu       sync.RWMutex
	ca         *egress.CA
	capacityMu sync.RWMutex
	dataDir    string
	tunnelPort int
}

// NewLocal constructs a node around a driver and its tunnel endpoint.
func NewLocal(driver sandbox.Driver, hub *tunnel.Hub) *Local {
	n := &Local{driver: driver, hub: hub, incoming: make(chan incomingStream), proxy: &proxyDirectory{requests: make(chan interceptEvent)}, tokens: make(map[string]string)}
	if hub != nil {
		hub.HandleIncoming(func(ctx context.Context, s *tunnel.Session, stream net.Conn, o tunnel.Open) {
			done := make(chan struct{})
			select {
			case n.incoming <- incomingStream{Incoming{AgentID: s.AgentID, Open: o}, stream, done}:
				select {
				case <-done:
				case <-ctx.Done():
					_ = stream.Close()
				}
			case <-ctx.Done():
			}
		})
	}
	return n
}

// Name implements Node.
func (n *Local) Name(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return n.driver.Name(), nil
}

// Create implements Node.
func (n *Local) Create(ctx context.Context, s sandbox.Spec) (sandbox.ID, error) {
	return n.driver.Create(ctx, s)
}

// Start implements Node.
func (n *Local) Start(ctx context.Context, id sandbox.ID, b sandbox.Boot) error {
	if n.tunnelPort != 0 {
		// sandboxd dials this node's tunnel endpoint, whatever the controller
		// has configured for its own.
		b.TunnelPort = n.tunnelPort
	}
	if ca := n.egressCA(); ca != nil {
		files := make(map[string][]byte, len(b.Files))
		for path, data := range b.Files {
			files[path] = append([]byte(nil), data...)
		}
		for path, data := range ca.BootFiles() {
			files[path] = append([]byte(nil), data...)
		}
		b.Files = files
	}
	return n.driver.Start(ctx, id, b)
}

// Stop implements Node.
func (n *Local) Stop(ctx context.Context, id sandbox.ID) error { return n.driver.Stop(ctx, id) }

// Destroy implements Node.
func (n *Local) Destroy(ctx context.Context, id sandbox.ID, o sandbox.DestroyOpts) error {
	return n.driver.Destroy(ctx, id, o)
}

// Status implements Node.
func (n *Local) Status(ctx context.Context, id sandbox.ID) (sandbox.Status, error) {
	return n.driver.Status(ctx, id)
}

// Rebuild implements Node.
func (n *Local) Rebuild(ctx context.Context, id sandbox.ID, s sandbox.Spec) error {
	return n.driver.Rebuild(ctx, id, s)
}

// Clone implements Node.
func (n *Local) Clone(ctx context.Context, src sandbox.ID, s sandbox.Spec) (sandbox.ID, error) {
	return n.driver.Clone(ctx, src, s)
}

// Attach implements Node.
func (n *Local) Attach(ctx context.Context, id sandbox.ID) error {
	if a, ok := n.driver.(sandbox.Attacher); ok {
		return a.Attach(ctx, id)
	}
	return ctx.Err()
}

// WaitConnected implements Node.
func (n *Local) WaitConnected(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.hub == nil {
		return nil
	}
	return n.hub.WaitConnected(ctx, id)
}

// Connected implements Node.
func (n *Local) Connected(ctx context.Context, id string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if n.hub == nil {
		return false, nil
	}
	s, ok := n.hub.Session(id)
	return ok && !s.Closed(), nil
}

// Disconnect implements Node.
func (n *Local) Disconnect(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if n.hub != nil {
		n.hub.Disconnect(id)
	}
	return nil
}

// OpenStream implements Node.
func (n *Local) OpenStream(ctx context.Context, id string, o tunnel.Open) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if n.hub == nil {
		return nil, tunnel.ErrNotConnected
	}
	s, ok := n.hub.Session(id)
	if !ok {
		return nil, tunnel.ErrNotConnected
	}
	return s.Open(ctx, o)
}

// Client constructs the controller-side sandboxd protocol client.
func Client(n Node, agentID string) *tunnel.Session {
	return tunnel.NewClient(agentID, func(ctx context.Context, o tunnel.Open) (io.ReadWriteCloser, error) {
		stream, err := n.OpenStream(ctx, agentID, o)
		if err == nil && stream == nil {
			return nil, errors.New("node returned a nil stream")
		}
		return stream, err
	})
}

// NextIncoming implements Node.
func (n *Local) NextIncoming(ctx context.Context) (Incoming, io.ReadWriteCloser, error) {
	select {
	case event := <-n.incoming:
		return event.info, &ownedStream{ReadWriteCloser: event.stream, done: event.done}, nil
	case <-ctx.Done():
		return Incoming{}, nil, ctx.Err()
	}
}

// SetTunnelToken authorizes a boot token hash, or removes it when hash is empty.
func (n *Local) SetTunnelToken(ctx context.Context, id, hash string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.tokensMu.Lock()
	defer n.tokensMu.Unlock()
	if hash == "" {
		delete(n.tokens, id)
	} else {
		n.tokens[id] = hash
	}
	return nil
}

// AuthenticateTunnel validates sandboxd at the node without a controller callback.
func (n *Local) AuthenticateTunnel(ctx context.Context, id, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	n.tokensMu.RLock()
	hash := n.tokens[id]
	n.tokensMu.RUnlock()
	sum := sha256.Sum256([]byte(token))
	actual := hex.EncodeToString(sum[:])
	if hash == "" || subtle.ConstantTimeCompare([]byte(hash), []byte(actual)) != 1 {
		return errors.New("invalid boot token")
	}
	return nil
}

// SetCA configures the node's egress CA. Sandboxes started before a CA is
// set do not trust the proxy.
func (n *Local) SetCA(ca *egress.CA) {
	n.caMu.Lock()
	defer n.caMu.Unlock()
	n.ca = ca
}

func (n *Local) egressCA() *egress.CA {
	n.caMu.RLock()
	defer n.caMu.RUnlock()
	return n.ca
}

// CACertificate implements Node.
func (n *Local) CACertificate(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ca := n.egressCA()
	if ca == nil {
		return nil, errors.New("egress CA unavailable")
	}
	return append([]byte(nil), ca.CertPEM()...), nil
}

// Capacity reports logical CPUs, currently available host memory and disk and
// the host's total memory.
// DiskBytes is space available to an unprivileged user on the data filesystem.
type Capacity struct {
	CPUs        int
	MemoryBytes uint64
	DiskBytes   uint64
	// MemoryTotalBytes is the host's installed memory (zero when the node does
	// not report it, as nodes of earlier releases do not).
	MemoryTotalBytes uint64 `json:",omitempty"`
	// DiskAllocatedBytes is the disk the machines' files on the node occupy
	// (zero when the driver does not account for it).
	DiskAllocatedBytes uint64 `json:",omitempty"`
}

// SetTunnelPort sets the port of this node's tunnel endpoint, used in every
// boot configuration. Configure it before starting sandboxes.
func (n *Local) SetTunnelPort(port int) { n.tunnelPort = port }

// SetDataDir selects the filesystem whose available disk space is reported.
// Configure it before reporting capacity; it must already exist.
func (n *Local) SetDataDir(path string) {
	n.capacityMu.Lock()
	defer n.capacityMu.Unlock()
	n.dataDir = path
}

// Capacity implements Node.
func (n *Local) Capacity(ctx context.Context) (Capacity, error) {
	if err := ctx.Err(); err != nil {
		return Capacity{}, err
	}
	n.capacityMu.RLock()
	dir := n.dataDir
	n.capacityMu.RUnlock()
	if dir == "" {
		return Capacity{}, errors.New("node data directory is not configured")
	}
	c, err := hostCapacity(ctx, dir)
	if err != nil {
		return c, err
	}
	if a, ok := n.driver.(sandbox.DiskAccounter); ok {
		// Best effort: a failure leaves the figure unreported.
		c.DiskAllocatedBytes, _ = a.AllocatedDiskBytes(ctx)
	}
	return c, nil
}
