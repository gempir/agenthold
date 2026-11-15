package node

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/id"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/tunnel"
)

// LocalID is the node id of the controller's built-in node.
const LocalID = "local"

// ErrNodeInUse is returned when removing a node that still hosts agents.
var ErrNodeInUse = errors.New("the node still hosts agents")

// Info is the controller's view of one node.
type Info struct {
	ID        string
	Name      string
	Local     bool
	Connected bool
	Capacity  Capacity
	LastSeen  time.Time
	Agents    int
}

// JoinToken is a newly created join token. Token is shown once.
type JoinToken struct {
	Token     string
	ExpiresAt time.Time
}

type unreachableError struct{ name string }

func (e unreachableError) Error() string {
	return fmt.Sprintf("node %s is offline; its agents are unreachable until it reconnects", e.name)
}
func (unreachableError) Is(target error) bool { return target == sandbox.ErrUnreachable }

// ClusterConfig configures a Cluster.
type ClusterConfig struct {
	Store *store.Store
	// Local is the built-in node, nil when this host does not run VMs.
	Local     Node
	LocalName string
	// CA is pushed to every edge node for TLS interception.
	CA *egress.CA
	// Endpoint (host:port) and Fingerprint of the node listener, embedded in
	// join tokens. An empty Endpoint disables join tokens.
	Endpoint    string
	Fingerprint string
	// HeartbeatInterval is how often capacity is refreshed. Default 30s.
	HeartbeatInterval time.Duration
	Log               *slog.Logger
}

// Cluster is the controller's Node: it routes every operation to the node an
// agent was placed on, places new agents, and fans in node-originated
// streams. Edge nodes attach through Serve after enrollment authenticated them.
type Cluster struct {
	cfg ClusterConfig
	log *slog.Logger
	now func() time.Time

	mu         sync.Mutex
	remotes    map[string]*Remote
	onConnect  []func(ctx context.Context, nodeID string)
	incoming   chan incomingItem
	intercepts chan interceptItem
}

type incomingItem struct {
	info   Incoming
	stream io.ReadWriteCloser
}

type interceptItem struct {
	info   InterceptRequest
	stream io.ReadWriteCloser
}

var (
	_ Node                 = (*Cluster)(nil)
	_ sandbox.Placer       = (*Cluster)(nil)
	_ sandbox.Reachability = (*Cluster)(nil)
	_ enrollment.Registry  = (*Cluster)(nil)
)

// NewCluster records the local node (or forgets an unused one) and starts
// background work that ends with ctx.
func NewCluster(ctx context.Context, cfg ClusterConfig) (*Cluster, error) {
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.HeartbeatInterval == 0 {
		cfg.HeartbeatInterval = 30 * time.Second
	}
	if cfg.LocalName == "" {
		cfg.LocalName = LocalID
	}
	c := &Cluster{
		cfg: cfg, log: cfg.Log, now: time.Now, remotes: map[string]*Remote{},
		incoming: make(chan incomingItem), intercepts: make(chan interceptItem),
	}
	st := cfg.Store
	if err := st.SetRemoteNodesOffline(ctx); err != nil {
		return nil, err
	}
	if cfg.Local != nil {
		if err := st.UpsertLocalNode(ctx, db.UpsertLocalNodeParams{Name: cfg.LocalName, Now: c.now().Unix()}); err != nil {
			return nil, err
		}
		c.pump(ctx, cfg.Local)
	} else {
		n, err := st.CountNodeSandboxes(ctx, LocalID)
		if err != nil {
			return nil, err
		}
		if n == 0 {
			if err := st.DeleteNode(ctx, LocalID); err != nil {
				return nil, err
			}
		} else if err := st.SetNodeState(ctx, db.SetNodeStateParams{State: "offline", ID: LocalID}); err != nil {
			return nil, err
		}
	}
	go c.heartbeat(ctx)
	return c, nil
}

// OnConnect registers fn to run (in its own goroutine) every time an edge node
// connects, after the egress CA was pushed: push its config, reconcile.
func (c *Cluster) OnConnect(fn func(ctx context.Context, nodeID string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onConnect = append(c.onConnect, fn)
}

// pump forwards a node's incoming and intercept streams until it disconnects.
func (c *Cluster) pump(ctx context.Context, n Node) {
	go func() {
		for {
			info, s, err := n.NextIncoming(ctx)
			if err != nil {
				return
			}
			select {
			case c.incoming <- incomingItem{info, s}:
			case <-ctx.Done():
				_ = s.Close()
				return
			}
		}
	}()
	go func() {
		for {
			info, s, err := n.NextIntercept(ctx)
			if err != nil {
				return
			}
			select {
			case c.intercepts <- interceptItem{info, s}:
			case <-ctx.Done():
				_ = s.Close()
				return
			}
		}
	}()
}

// Serve runs an authenticated edge node connection until it breaks or ctx
// ends. A newer connection from the same node replaces an older one.
func (c *Cluster) Serve(ctx context.Context, nodeID string, conn net.Conn) {
	r, err := AcceptRemote(ctx, conn)
	if err != nil {
		_ = conn.Close()
		return
	}
	defer r.Close()
	setup, cancel := context.WithTimeout(ctx, 30*time.Second)
	if c.cfg.CA != nil {
		err = r.SetEgressCA(setup, c.cfg.CA.CertPEM(), c.cfg.CA.KeyPEM())
	}
	cancel()
	if err != nil {
		c.log.Warn("node setup failed", "node", nodeID, "err", err)
		return
	}
	c.mu.Lock()
	old := c.remotes[nodeID]
	c.remotes[nodeID] = r
	hooks := slices.Clone(c.onConnect)
	c.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
	c.pump(ctx, r)
	_ = c.cfg.Store.SetNodeState(ctx, db.SetNodeStateParams{State: "online", LastSeenAt: c.now().Unix(), ID: nodeID})
	c.refresh(ctx, nodeID, r)
	c.log.Info("node connected", "node", nodeID, "remote", conn.RemoteAddr().String())
	for _, fn := range hooks {
		go fn(ctx, nodeID)
	}
	select {
	case <-r.Done():
	case <-ctx.Done():
	}
	c.mu.Lock()
	current := c.remotes[nodeID] == r
	if current {
		delete(c.remotes, nodeID)
	}
	c.mu.Unlock()
	if current {
		bg, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = c.cfg.Store.SetNodeState(bg, db.SetNodeStateParams{State: "offline", LastSeenAt: c.now().Unix(), ID: nodeID})
		c.log.Warn("node disconnected", "node", nodeID)
	}
}

func (c *Cluster) remote(nodeID string) *Remote {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.remotes[nodeID]
}

// connected returns the reachable nodes by id.
func (c *Cluster) connected() map[string]Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]Node, len(c.remotes)+1)
	for id, r := range c.remotes {
		out[id] = r
	}
	if c.cfg.Local != nil {
		out[LocalID] = c.cfg.Local
	}
	return out
}

func (c *Cluster) heartbeat(ctx context.Context) {
	t := time.NewTicker(c.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		for id, n := range c.connected() {
			c.refresh(ctx, id, n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refresh records a node's capacity and that it was seen.
func (c *Cluster) refresh(ctx context.Context, nodeID string, n Node) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	capacity, err := n.Capacity(cctx)
	if err != nil {
		c.log.Debug("node capacity", "node", nodeID, "err", err)
		return
	}
	_ = c.cfg.Store.SetNodeCapacity(ctx, db.SetNodeCapacityParams{
		Cpus: int64(capacity.CPUs), MemoryBytes: clampInt64(capacity.MemoryBytes), DiskBytes: clampInt64(capacity.DiskBytes),
		LastSeenAt: c.now().Unix(), ID: nodeID,
	})
}

func clampInt64(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}

func (c *Cluster) byNode(ctx context.Context, nodeID string) (Node, error) {
	if nodeID == "" || nodeID == LocalID {
		if c.cfg.Local != nil {
			return c.cfg.Local, nil
		}
		return nil, unreachableError{"local"}
	}
	if r := c.remote(nodeID); r != nil {
		return r, nil
	}
	name := nodeID
	if n, err := c.cfg.Store.GetNode(ctx, nodeID); err == nil {
		name = n.Name
	}
	return nil, unreachableError{name}
}

func (c *Cluster) forAgent(ctx context.Context, agentID string) (Node, error) {
	sb, err := c.cfg.Store.GetSandbox(ctx, agentID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sandbox.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return c.byNode(ctx, sb.NodeID)
}

func (c *Cluster) forSandbox(ctx context.Context, sid sandbox.ID) (Node, error) {
	sb, err := c.cfg.Store.GetSandboxByDriverID(ctx, string(sid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, sandbox.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	return c.byNode(ctx, sb.NodeID)
}

// Reachable implements sandbox.Reachability.
func (c *Cluster) Reachable(ctx context.Context, agentID string) error {
	_, err := c.forAgent(ctx, agentID)
	return err
}

// Place implements sandbox.Placer: the connected node with the most available
// memory (then disk) gets the agent. Nodes that cannot report capacity are
// skipped.
func (c *Cluster) Place(ctx context.Context) (sandbox.Placement, error) {
	type candidate struct {
		id  string
		n   Node
		cap Capacity
	}
	nodes := c.connected()
	results := make(chan *candidate, len(nodes))
	for id, n := range nodes {
		go func() {
			cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			capacity, err := n.Capacity(cctx)
			if err != nil {
				c.log.Warn("placement: node capacity", "node", id, "err", err)
				results <- nil
				return
			}
			results <- &candidate{id, n, capacity}
		}()
	}
	var all []candidate
	for range nodes {
		if r := <-results; r != nil {
			all = append(all, *r)
		}
	}
	if len(all) == 0 {
		return sandbox.Placement{}, fmt.Errorf("%w: connect an edge node or run the controller on a host with /dev/kvm", sandbox.ErrNoCapacity)
	}
	best := slices.MaxFunc(all, func(a, b candidate) int {
		return cmp.Or(cmp.Compare(a.cap.MemoryBytes, b.cap.MemoryBytes), cmp.Compare(a.cap.DiskBytes, b.cap.DiskBytes), cmp.Compare(b.id, a.id))
	})
	name, err := best.n.Name(ctx)
	if err != nil {
		return sandbox.Placement{}, err
	}
	return sandbox.Placement{NodeID: best.id, Driver: name}, nil
}

// Nodes lists every registered node with its live connection state.
func (c *Cluster) Nodes(ctx context.Context) ([]Info, error) {
	rows, err := c.cfg.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	sbs, err := c.cfg.Store.ListSandboxes(ctx)
	if err != nil {
		return nil, err
	}
	agents := map[string]int{}
	for _, sb := range sbs {
		nodeID := sb.NodeID
		if nodeID == "" {
			nodeID = LocalID
		}
		agents[nodeID]++
	}
	live := c.connected()
	out := make([]Info, 0, len(rows))
	for _, r := range rows {
		_, connected := live[r.ID]
		info := Info{
			ID: r.ID, Name: r.Name, Local: r.ID == LocalID, Connected: connected, Agents: agents[r.ID],
			Capacity: Capacity{CPUs: int(r.Cpus), MemoryBytes: uint64(max(r.MemoryBytes, 0)), DiskBytes: uint64(max(r.DiskBytes, 0))},
		}
		if r.LastSeenAt > 0 {
			info.LastSeen = time.Unix(r.LastSeenAt, 0).UTC()
		}
		out = append(out, info)
	}
	return out, nil
}

// CreateJoinToken issues a single-use token valid for ttl. A non-empty name
// overrides the name the node proposes when it joins.
func (c *Cluster) CreateJoinToken(ctx context.Context, createdBy, name string, ttl time.Duration) (JoinToken, error) {
	if c.cfg.Endpoint == "" {
		return JoinToken{}, errors.New("edge nodes are disabled on this controller (nodes.listen is off)")
	}
	tok, err := enrollment.NewToken(c.cfg.Endpoint, c.cfg.Fingerprint)
	if err != nil {
		return JoinToken{}, err
	}
	now := c.now()
	expires := now.Add(ttl)
	if err := c.cfg.Store.DeleteStaleNodeJoinTokens(ctx, now.Unix()); err != nil {
		return JoinToken{}, err
	}
	if err := c.cfg.Store.CreateNodeJoinToken(ctx, db.CreateNodeJoinTokenParams{
		TokenHash: enrollment.HashSecret(tok.Secret), Name: name, CreatedBy: createdBy,
		CreatedAt: now.Unix(), ExpiresAt: expires.Unix(),
	}); err != nil {
		return JoinToken{}, err
	}
	return JoinToken{Token: tok.String(), ExpiresAt: expires.UTC()}, nil
}

// Join implements enrollment.Registry.
func (c *Cluster) Join(ctx context.Context, secretHash, fingerprint, name string) (enrollment.Peer, error) {
	var peer enrollment.Peer
	err := c.cfg.Store.Tx(ctx, func(q *db.Queries) error {
		now := c.now().Unix()
		tokenName, err := q.ConsumeNodeJoinToken(ctx, db.ConsumeNodeJoinTokenParams{TokenHash: secretHash, Now: now})
		if errors.Is(err, sql.ErrNoRows) {
			return enrollment.ErrDenied
		} else if err != nil {
			return err
		}
		if _, err := q.GetNodeByFingerprint(ctx, fingerprint); err == nil {
			return enrollment.ErrDenied // one certificate, one node
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if tokenName != "" {
			name = tokenName
		}
		peer = enrollment.Peer{ID: id.New("node"), Name: name}
		return q.CreateNode(ctx, db.CreateNodeParams{ID: peer.ID, Name: peer.Name, CertFingerprint: fingerprint, CreatedAt: now})
	})
	if err == nil {
		c.log.Info("node joined", "node", peer.ID, "name", peer.Name)
	}
	return peer, err
}

// Authenticate implements enrollment.Registry.
func (c *Cluster) Authenticate(ctx context.Context, fingerprint string) (enrollment.Peer, error) {
	n, err := c.cfg.Store.GetNodeByFingerprint(ctx, fingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return enrollment.Peer{}, enrollment.ErrDenied
	} else if err != nil {
		return enrollment.Peer{}, err
	}
	return enrollment.Peer{ID: n.ID, Name: n.Name}, nil
}

// RemoveNode deletes an edge node and disconnects it. It refuses while the
// node hosts agents unless force is set and the node is offline; forcing
// forgets those agents (their disks stay on the lost machine).
func (c *Cluster) RemoveNode(ctx context.Context, nodeID string, force bool) error {
	if nodeID == LocalID {
		return errors.New("the built-in local node cannot be removed")
	}
	if _, err := c.cfg.Store.GetNode(ctx, nodeID); err != nil {
		return err
	}
	sbs, err := c.cfg.Store.ListNodeSandboxes(ctx, nodeID)
	if err != nil {
		return err
	}
	if len(sbs) > 0 {
		if !force {
			return fmt.Errorf("%w (%d); delete them first", ErrNodeInUse, len(sbs))
		}
		if c.remote(nodeID) != nil {
			return fmt.Errorf("%w (%d) and is online; delete them first", ErrNodeInUse, len(sbs))
		}
	}
	err = c.cfg.Store.Tx(ctx, func(q *db.Queries) error {
		for _, sb := range sbs {
			if err := q.DeleteAgent(ctx, sb.AgentID); err != nil {
				return err
			}
		}
		return q.DeleteNode(ctx, nodeID)
	})
	if err != nil {
		return err
	}
	if r := c.remote(nodeID); r != nil {
		_ = r.Close()
	}
	c.log.Info("node removed", "node", nodeID, "forgotten_agents", len(sbs))
	return nil
}

// Name implements Node. The cluster has no single driver; see Place.
func (c *Cluster) Name(ctx context.Context) (string, error) { return "cluster", ctx.Err() }

// Create implements Node on the node the agent was placed on.
func (c *Cluster) Create(ctx context.Context, s sandbox.Spec) (sandbox.ID, error) {
	n, err := c.forAgent(ctx, s.AgentID)
	if err != nil {
		return "", err
	}
	return n.Create(ctx, s)
}

// Start implements Node.
func (c *Cluster) Start(ctx context.Context, sid sandbox.ID, b sandbox.Boot) error {
	n, err := c.forSandbox(ctx, sid)
	if err != nil {
		return err
	}
	return n.Start(ctx, sid, b)
}

// Stop implements Node.
func (c *Cluster) Stop(ctx context.Context, sid sandbox.ID) error {
	n, err := c.forSandbox(ctx, sid)
	if err != nil {
		return err
	}
	return n.Stop(ctx, sid)
}

// Destroy implements Node.
func (c *Cluster) Destroy(ctx context.Context, sid sandbox.ID, o sandbox.DestroyOpts) error {
	n, err := c.forSandbox(ctx, sid)
	if err != nil {
		return err
	}
	return n.Destroy(ctx, sid, o)
}

// Status implements Node.
func (c *Cluster) Status(ctx context.Context, sid sandbox.ID) (sandbox.Status, error) {
	n, err := c.forSandbox(ctx, sid)
	if errors.Is(err, sandbox.ErrNotFound) {
		return sandbox.Status{State: sandbox.StateMissing}, nil
	} else if err != nil {
		return sandbox.Status{}, err
	}
	return n.Status(ctx, sid)
}

// Rebuild implements Node.
func (c *Cluster) Rebuild(ctx context.Context, sid sandbox.ID, s sandbox.Spec) error {
	n, err := c.forSandbox(ctx, sid)
	if err != nil {
		return err
	}
	return n.Rebuild(ctx, sid, s)
}

// Attach implements Node.
func (c *Cluster) Attach(ctx context.Context, sid sandbox.ID) error {
	n, err := c.forSandbox(ctx, sid)
	if err != nil {
		return err
	}
	return n.Attach(ctx, sid)
}

// WaitConnected implements Node.
func (c *Cluster) WaitConnected(ctx context.Context, agentID string) error {
	n, err := c.forAgent(ctx, agentID)
	if err != nil {
		return err
	}
	return n.WaitConnected(ctx, agentID)
}

// Connected implements Node. Agents of a disconnected node are not connected.
func (c *Cluster) Connected(ctx context.Context, agentID string) (bool, error) {
	n, err := c.forAgent(ctx, agentID)
	if errors.Is(err, sandbox.ErrUnreachable) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return n.Connected(ctx, agentID)
}

// Disconnect implements Node.
func (c *Cluster) Disconnect(ctx context.Context, agentID string) error {
	n, err := c.forAgent(ctx, agentID)
	if errors.Is(err, sandbox.ErrUnreachable) {
		return nil // nothing is connected through a lost node
	} else if err != nil {
		return err
	}
	return n.Disconnect(ctx, agentID)
}

// OpenStream implements Node.
func (c *Cluster) OpenStream(ctx context.Context, agentID string, o tunnel.Open) (io.ReadWriteCloser, error) {
	n, err := c.forAgent(ctx, agentID)
	if err != nil {
		return nil, errors.Join(err, tunnel.ErrNotConnected)
	}
	return n.OpenStream(ctx, agentID, o)
}

// SetTunnelToken implements Node.
func (c *Cluster) SetTunnelToken(ctx context.Context, agentID, hash string) error {
	n, err := c.forAgent(ctx, agentID)
	if err != nil {
		return err
	}
	return n.SetTunnelToken(ctx, agentID, hash)
}

// NextIncoming implements Node for all connected nodes.
func (c *Cluster) NextIncoming(ctx context.Context) (Incoming, io.ReadWriteCloser, error) {
	select {
	case e := <-c.incoming:
		return e.info, e.stream, nil
	case <-ctx.Done():
		return Incoming{}, nil, ctx.Err()
	}
}

// NextIntercept implements Node for all connected nodes.
func (c *Cluster) NextIntercept(ctx context.Context) (InterceptRequest, io.ReadWriteCloser, error) {
	select {
	case e := <-c.intercepts:
		return e.info, e.stream, nil
	case <-ctx.Done():
		return InterceptRequest{}, nil, ctx.Err()
	}
}

// SetAgentConfigs implements Node. Each connected node receives only the
// configuration (and so the decrypted secrets) of its own agents.
func (c *Cluster) SetAgentConfigs(ctx context.Context, configs []AgentConfig) error {
	sbs, err := c.cfg.Store.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	nodeOf := make(map[string]string, len(sbs))
	for _, sb := range sbs {
		nodeOf[sb.AgentID] = cmp.Or(sb.NodeID, LocalID)
	}
	byNode := map[string][]AgentConfig{}
	for _, cfg := range configs {
		if nodeID, ok := nodeOf[cfg.ID]; ok {
			byNode[nodeID] = append(byNode[nodeID], cfg)
		}
	}
	var errs []error
	for nodeID, n := range c.connected() {
		if err := n.SetAgentConfigs(ctx, byNode[nodeID]); err != nil && !errors.Is(err, sandbox.ErrUnreachable) {
			errs = append(errs, fmt.Errorf("node %s: %w", nodeID, err))
		}
	}
	return errors.Join(errs...)
}

// CACertificate implements Node: all nodes share the controller's egress CA.
func (c *Cluster) CACertificate(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.cfg.CA != nil {
		return append([]byte(nil), c.cfg.CA.CertPEM()...), nil
	}
	if c.cfg.Local != nil {
		return c.cfg.Local.CACertificate(ctx)
	}
	return nil, errors.New("egress CA unavailable")
}

// Capacity implements Node as the sum over connected nodes.
func (c *Cluster) Capacity(ctx context.Context) (Capacity, error) {
	var total Capacity
	for _, n := range c.connected() {
		capacity, err := n.Capacity(ctx)
		if err != nil {
			continue
		}
		total.CPUs += capacity.CPUs
		total.MemoryBytes += capacity.MemoryBytes
		total.DiskBytes += capacity.DiskBytes
	}
	return total, ctx.Err()
}
