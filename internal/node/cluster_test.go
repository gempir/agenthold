package node

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/node/enrollment"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/sandbox/fake"
	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// sizedNode reports a fixed amount of free memory.
type sizedNode struct {
	*Local
	memory uint64
}

func (n *sizedNode) Capacity(context.Context) (Capacity, error) {
	return Capacity{CPUs: 4, MemoryBytes: n.memory, DiskBytes: 1 << 30, MemoryTotalBytes: 8 << 30, DiskAllocatedBytes: 5 << 20}, nil
}

type edge struct {
	id      string
	backend *sizedNode
	cancel  context.CancelFunc
	served  chan struct{}
}

type clusterFixture struct {
	t       *testing.T
	st      *store.Store
	c       *Cluster
	connect chan string
	// version is what edges announce when they connect.
	version string
}

func newClusterFixture(t *testing.T, local Node) *clusterFixture {
	t.Helper()
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	c, err := NewCluster(t.Context(), ClusterConfig{Store: st, Local: local, Endpoint: "127.0.0.1:1", Fingerprint: "f"})
	if err != nil {
		t.Fatal(err)
	}
	f := &clusterFixture{t: t, st: st, c: c, connect: make(chan string, 16)}
	c.OnConnect(func(_ context.Context, id string) { f.connect <- id })
	if err := st.CreateUser(t.Context(), db.CreateUserParams{ID: "u", Name: "owner"}); err != nil {
		t.Fatal(err)
	}
	return f
}

// addEdge registers a node and connects backend to the cluster over a pipe.
func (f *clusterFixture) addEdge(id string, memory uint64) *edge {
	f.t.Helper()
	if err := f.st.CreateNode(f.t.Context(), db.CreateNodeParams{ID: id, Name: id + "-name", CertFingerprint: "fp-" + id, CreatedAt: 1}); err != nil {
		f.t.Fatal(err)
	}
	local := NewLocal(fake.New(f.t.TempDir()), nil)
	local.SetDataDir(f.t.TempDir())
	e := &edge{id: id, backend: &sizedNode{Local: local, memory: memory}}
	f.reconnect(e)
	return e
}

func (f *clusterFixture) reconnect(e *edge) {
	f.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	a, b := net.Pipe()
	e.cancel, e.served = cancel, make(chan struct{})
	go func() { _ = ServeRemote(ctx, b, e.backend, ServeOptions{}) }()
	go func() {
		defer close(e.served)
		f.c.ServePeer(ctx, enrollment.Peer{ID: e.id, Name: e.id, Version: f.version}, a)
	}()
	f.t.Cleanup(func() { cancel(); <-e.served })
	select {
	case id := <-f.connect:
		if id != e.id {
			f.t.Fatalf("connected %s, want %s", id, e.id)
		}
	case <-time.After(5 * time.Second):
		f.t.Fatal("node did not connect")
	}
}

// disconnect drops the node's connection as a network failure would.
func (f *clusterFixture) disconnect(e *edge) {
	e.cancel()
	<-e.served
}

func (f *clusterFixture) agent(id, nodeID string) {
	f.t.Helper()
	err := f.st.Tx(f.t.Context(), func(q *db.Queries) error {
		if err := q.CreateAgent(f.t.Context(), db.CreateAgentParams{ID: id, OwnerID: "u", Name: id, Image: "base"}); err != nil {
			return err
		}
		return q.CreateSandbox(f.t.Context(), db.CreateSandboxParams{AgentID: id, NodeID: nodeID, Driver: "fake", State: "creating"})
	})
	if err != nil {
		f.t.Fatal(err)
	}
}

func TestPlacementPicksMostFreeNodeAndAgentsStay(t *testing.T) {
	f := newClusterFixture(t, nil)
	small := f.addEdge("node_small", 1<<30)
	big := f.addEdge("node_big", 8<<30)
	p, err := f.c.Place(t.Context())
	if err != nil || p.NodeID != big.id || p.Driver != "fake" {
		t.Fatalf("placement %+v %v", p, err)
	}
	m := sandbox.NewManagerWithNode(f.st, f.c, sandbox.BaseSpec, sandbox.Options{}, nil)
	defer m.Close()
	var start func()
	err = f.st.Tx(t.Context(), func(q *db.Queries) error {
		if err := q.CreateAgent(t.Context(), db.CreateAgentParams{ID: "ag", OwnerID: "u", Name: "ag", Image: "base"}); err != nil {
			return err
		}
		start, err = m.Provision(t.Context(), q, "ag")
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	start()
	m.Wait()
	sb, err := f.st.GetSandbox(t.Context(), "ag")
	if err != nil || sb.NodeID != big.id || sb.State != string(sandbox.PhaseRunning) {
		t.Fatalf("sandbox %+v %v", sb, err)
	}
	if st, _ := big.backend.Status(t.Context(), sandbox.ID(sb.DriverID)); st.State != sandbox.StateRunning {
		t.Fatalf("agent not running on its node: %v", st)
	}
	if st, _ := small.backend.Status(t.Context(), sandbox.ID(sb.DriverID)); st.State != sandbox.StateMissing {
		t.Fatalf("agent leaked to another node: %v", st)
	}
	// The big node fills up: new agents go elsewhere, the existing one stays.
	big.backend.memory = 0
	if p, _ = f.c.Place(t.Context()); p.NodeID != small.id {
		t.Fatalf("placement after fill: %+v", p)
	}
	if err = m.Stop(t.Context(), "ag"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if st, _ := big.backend.Status(t.Context(), sandbox.ID(sb.DriverID)); st.State != sandbox.StateStopped {
		t.Fatalf("stop did not reach the agent's node: %v", st)
	}
}

func TestPlaceOnPinsAgentToTheChosenNode(t *testing.T) {
	f := newClusterFixture(t, nil)
	small := f.addEdge("node_small", 1<<30)
	big := f.addEdge("node_big", 8<<30)
	// Automatic placement would pick the big node; the request wins.
	if p, err := f.c.PlaceOn(t.Context(), sandbox.PlaceRequest{NodeID: small.id, MemoryMB: 512}); err != nil || p.NodeID != small.id || p.Driver != "fake" {
		t.Fatalf("pinned placement %+v %v", p, err)
	}

	m := sandbox.NewManagerWithNode(f.st, f.c, sandbox.BaseSpec, sandbox.Options{}, nil)
	defer m.Close()
	place, err := m.PlaceNew(t.Context(), sandbox.PlaceRequest{NodeID: small.id, MemoryMB: 512})
	if err != nil {
		t.Fatal(err)
	}
	var start func()
	err = f.st.Tx(t.Context(), func(q *db.Queries) error {
		if err := q.CreateAgent(t.Context(), db.CreateAgentParams{ID: "ag", OwnerID: "u", Name: "ag", Image: "base"}); err != nil {
			return err
		}
		start, err = m.ProvisionAt(t.Context(), q, "ag", place)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	start()
	m.Wait()
	sb, err := f.st.GetSandbox(t.Context(), "ag")
	if err != nil || sb.NodeID != small.id || sb.State != string(sandbox.PhaseRunning) {
		t.Fatalf("sandbox %+v %v", sb, err)
	}
	if st, _ := big.backend.Status(t.Context(), sandbox.ID(sb.DriverID)); st.State != sandbox.StateMissing {
		t.Fatalf("agent leaked to another node: %v", st)
	}
}

func TestPlaceOnRefusesUnusableNodes(t *testing.T) {
	f := newClusterFixture(t, nil)
	small := f.addEdge("node_small", 1<<30)
	gone := f.addEdge("node_gone", 8<<30)
	f.disconnect(gone)

	if _, err := f.c.PlaceOn(t.Context(), sandbox.PlaceRequest{NodeID: "node_nope"}); !errors.Is(err, sandbox.ErrUnknownNode) {
		t.Fatalf("unknown node: %v", err)
	}
	var unavailable *sandbox.NodeUnavailableError
	if _, err := f.c.PlaceOn(t.Context(), sandbox.PlaceRequest{NodeID: gone.id}); !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "node_gone-name is offline") {
		t.Fatalf("offline node: %v", err)
	}
	// The node has 1 GiB available: 1024 MB fit, 1025 do not.
	if _, err := f.c.PlaceOn(t.Context(), sandbox.PlaceRequest{NodeID: small.id, MemoryMB: 1024}); err != nil {
		t.Fatalf("exactly enough memory: %v", err)
	}
	_, err := f.c.PlaceOn(t.Context(), sandbox.PlaceRequest{NodeID: small.id, MemoryMB: 1025})
	if !errors.As(err, &unavailable) || !strings.Contains(err.Error(), "1024 MB of memory available, the agent needs 1025 MB") {
		t.Fatalf("too little memory: %v", err)
	}
	// Automatic placement is unchanged by any of this.
	if p, err := f.c.Place(t.Context()); err != nil || p.NodeID != small.id {
		t.Fatalf("automatic placement %+v %v", p, err)
	}
}

func TestNoNodeMeansNoCapacity(t *testing.T) {
	f := newClusterFixture(t, nil)
	if _, err := f.c.Place(t.Context()); !errors.Is(err, sandbox.ErrNoCapacity) {
		t.Fatalf("place without nodes: %v", err)
	}
}

func TestDisconnectedNodeFailsFastAndReconnects(t *testing.T) {
	f := newClusterFixture(t, nil)
	e := f.addEdge("node_e", 1<<30)
	f.agent("ag", e.id)
	m := sandbox.NewManagerWithNode(f.st, f.c, sandbox.BaseSpec, sandbox.Options{}, nil)
	defer m.Close()
	if err := m.ReconcileNode(t.Context(), e.id); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if sb, _ := f.st.GetSandbox(t.Context(), "ag"); sb.State != string(sandbox.PhaseRunning) {
		t.Fatalf("reconcile did not create the agent: %+v", sb)
	}
	f.disconnect(e)
	began := time.Now()
	if err := m.Stop(t.Context(), "ag"); !errors.Is(err, sandbox.ErrUnreachable) {
		t.Fatalf("stop on offline node: %v", err)
	}
	if err := m.Destroy(t.Context(), "ag"); !errors.Is(err, sandbox.ErrUnreachable) {
		t.Fatalf("destroy on offline node: %v", err)
	}
	if ok, err := f.c.Connected(t.Context(), "ag"); ok || err != nil {
		t.Fatalf("connected through offline node: %v %v", ok, err)
	}
	if time.Since(began) > time.Second {
		t.Fatal("operations on an offline node must fail fast")
	}
	if sb, _ := f.st.GetSandbox(t.Context(), "ag"); sb.State != string(sandbox.PhaseRunning) {
		t.Fatalf("an offline node must not put its agents into error: %+v", sb)
	}
	infos, err := f.c.Nodes(t.Context())
	if err != nil || len(infos) != 1 || infos[0].Connected || infos[0].Agents != 1 {
		t.Fatalf("nodes while offline: %+v %v", infos, err)
	}
	if n, _ := f.st.GetNode(t.Context(), e.id); n.State != "offline" {
		t.Fatalf("persisted state: %s", n.State)
	}
	// Reconnect: the hook runs, the node is reachable again and the agent
	// still runs there (the VM survived the controller connection).
	f.reconnect(e)
	if err := f.c.Reachable(t.Context(), "ag"); err != nil {
		t.Fatal(err)
	}
	if err := m.ReconcileNode(t.Context(), e.id); err != nil {
		t.Fatal(err)
	}
	if err := m.Stop(t.Context(), "ag"); err != nil {
		t.Fatal(err)
	}
	m.Wait()
	if sb, _ := f.st.GetSandbox(t.Context(), "ag"); sb.State != string(sandbox.PhaseSleeping) {
		t.Fatalf("after reconnect: %+v", sb)
	}
}

func TestSecretsOnlyReachTheirNode(t *testing.T) {
	f := newClusterFixture(t, nil)
	a := f.addEdge("node_a", 1<<30)
	b := f.addEdge("node_b", 1<<30)
	f.agent("on-a", a.id)
	f.agent("on-b", b.id)
	cfg := func(id, subnet, value string) AgentConfig {
		return AgentConfig{ID: id, Subnet: netip.MustParsePrefix(subnet), Injections: []secrets.Injection{{Value: value}}}
	}
	if err := f.c.SetAgentConfigs(t.Context(), []AgentConfig{cfg("on-a", "10.9.0.0/30", "secret-a"), cfg("on-b", "10.9.0.4/30", "secret-b")}); err != nil {
		t.Fatal(err)
	}
	check := func(n *edge, ip string, want string) {
		t.Helper()
		got, err := n.backend.ProxyAgents().AgentForSource(t.Context(), netip.MustParseAddr(ip))
		switch {
		case err != nil:
			t.Fatal(err)
		case want == "" && got != nil:
			t.Fatalf("%s received another node's secret %+v", n.id, got.Injections)
		case want != "" && (got == nil || got.Injections[0].Value != want):
			t.Fatalf("%s: %+v", n.id, got)
		}
	}
	check(a, "10.9.0.2", "secret-a")
	check(a, "10.9.0.6", "")
	check(b, "10.9.0.6", "secret-b")
	check(b, "10.9.0.2", "")
}

func TestRemoveNode(t *testing.T) {
	f := newClusterFixture(t, nil)
	e := f.addEdge("node_r", 1<<30)
	f.agent("ag", e.id)
	if err := f.c.RemoveNode(t.Context(), e.id, false); !errors.Is(err, ErrNodeInUse) {
		t.Fatalf("remove with agents: %v", err)
	}
	if err := f.c.RemoveNode(t.Context(), e.id, true); !errors.Is(err, ErrNodeInUse) {
		t.Fatalf("force-remove an online node: %v", err)
	}
	f.disconnect(e)
	if err := f.c.RemoveNode(t.Context(), e.id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.GetAgent(t.Context(), "ag"); err == nil {
		t.Fatal("forced removal must forget the node's agents")
	}
	if err := f.c.RemoveNode(t.Context(), LocalID, false); err == nil {
		t.Fatal("removed the built-in node")
	}
}

func TestLocalNodeRowFollowsConfiguration(t *testing.T) {
	st, err := store.Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	local := NewLocal(fake.New(t.TempDir()), nil)
	local.SetDataDir(t.TempDir())
	var connects atomic.Int32
	c, err := NewCluster(t.Context(), ClusterConfig{Store: st, Local: local, LocalName: "box"})
	if err != nil {
		t.Fatal(err)
	}
	c.OnConnect(func(context.Context, string) { connects.Add(1) })
	if p, err := c.Place(t.Context()); err != nil || p.NodeID != LocalID {
		t.Fatalf("local placement %+v %v", p, err)
	}
	if infos, _ := c.Nodes(t.Context()); len(infos) != 1 || !infos[0].Local || !infos[0].Connected || infos[0].Name != "box" {
		t.Fatalf("nodes %+v", infos)
	}
	// Restarted without KVM and without local agents: the local node disappears.
	if _, err = NewCluster(t.Context(), ClusterConfig{Store: st}); err != nil {
		t.Fatal(err)
	}
	if infos, _ := c.Nodes(t.Context()); len(infos) != 0 {
		t.Fatalf("unused local node kept: %+v", infos)
	}
	if connects.Load() != 0 {
		t.Fatal("the local node is not an edge connection")
	}
}

// A node that connects and hangs up before it answers the setup call (`agenthold node` does this
// to ask its controller where it serves downloads from) must not disturb the node it belongs to.
func TestConnectAndImmediateCloseChangesNothing(t *testing.T) {
	f := newClusterFixture(t, nil)
	f.addEdge("e1", 8<<30)
	ca, err := egress.LoadOrCreateCA(t.TempDir()) // from now on connections must complete the CA setup call
	if err != nil {
		t.Fatal(err)
	}
	f.c.cfg.CA = ca
	a, b := net.Pipe()
	_ = b.Close()
	done := make(chan struct{})
	go func() { defer close(done); f.c.Serve(t.Context(), "e1", a) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return for a connection that went away")
	}
	if f.c.remote("e1") == nil || f.c.remote("e1").Done() == nil {
		t.Fatal("the connected node was replaced")
	}
	select {
	case <-f.c.remote("e1").Done():
		t.Fatal("the connected node was dropped")
	default:
	}
	n, err := f.st.GetNode(t.Context(), "e1")
	if err != nil || n.State != "online" {
		t.Fatalf("node state after the hang-up: %+v %v", n, err)
	}
	select {
	case id := <-f.connect:
		t.Fatalf("hang-up triggered the on-connect hooks for %s", id)
	default:
	}
}

// What a node reported is kept, so an offline node (and a restarted
// controller) still shows its version, memory and disk.
func TestNodeInfoIsPersisted(t *testing.T) {
	f := newClusterFixture(t, nil)
	f.version = "v1.2.3"
	e := f.addEdge("node_e", 1<<30)
	check := func(when string) {
		t.Helper()
		infos, err := f.c.Nodes(t.Context())
		if err != nil || len(infos) != 1 {
			t.Fatalf("%s: %+v %v", when, infos, err)
		}
		n := infos[0]
		if n.Version != "v1.2.3" || n.Capacity.MemoryTotalBytes != 8<<30 || n.Capacity.DiskAllocatedBytes != 5<<20 || n.Capacity.CPUs != 4 {
			t.Fatalf("%s: %+v", when, n)
		}
	}
	check("online")
	f.disconnect(e)
	for range 100 {
		if infos, _ := f.c.Nodes(t.Context()); len(infos) == 1 && !infos[0].Connected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	check("offline")
	// A restarted controller reads it from the database.
	c2, err := NewCluster(t.Context(), ClusterConfig{Store: f.st, Endpoint: "127.0.0.1:1", Fingerprint: "f"})
	if err != nil {
		t.Fatal(err)
	}
	infos, err := c2.Nodes(t.Context())
	if err != nil || len(infos) != 1 || infos[0].Connected || infos[0].Version != "v1.2.3" || infos[0].Capacity.MemoryTotalBytes != 8<<30 {
		t.Fatalf("after restart: %+v %v", infos, err)
	}
	// A node of an earlier release does not announce a version: it is forgotten.
	f.version = ""
	f.reconnect(e)
	if infos, _ := f.c.Nodes(t.Context()); infos[0].Version != "" {
		t.Fatalf("stale version kept: %+v", infos[0])
	}
}
