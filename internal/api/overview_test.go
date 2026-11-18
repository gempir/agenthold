package api_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/api"
	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/config"
	"github.com/gempir/agenthold/internal/tunnel"
)

// guestStats stands in for the sandboxd of every machine.
type guestStats struct {
	mu    sync.Mutex
	calls map[string]int
	busy  uint64 // ticks the guest's CPU was busy
	total uint64
	// hang makes the call of these machines block until its context ends;
	// stuck ignores the context too (until the test ends).
	hang  map[string]bool
	stuck map[string]chan struct{}
	fail  map[string]error
}

func newGuestStats() *guestStats {
	return &guestStats{calls: map[string]int{}, hang: map[string]bool{}, stuck: map[string]chan struct{}{}, fail: map[string]error{}}
}

func (g *guestStats) read(ctx context.Context, id string) (tunnel.Stats, error) {
	g.mu.Lock()
	g.calls[id]++
	hang, stuck, fail := g.hang[id], g.stuck[id], g.fail[id]
	g.busy += 50
	g.total += 100
	st := tunnel.Stats{
		CPUBusyTicks: g.busy, CPUTotalTicks: g.total,
		MemTotalBytes: 2 << 30, MemUsedBytes: 1 << 30,
		HomeTotalBytes: 10 << 30, HomeUsedBytes: 1 << 30, RootTotalBytes: 4 << 30, RootUsedBytes: 2 << 30,
	}
	g.mu.Unlock()
	switch {
	case stuck != nil:
		<-stuck
	case hang:
		<-ctx.Done()
		return tunnel.Stats{}, ctx.Err()
	case fail != nil:
		return tunnel.Stats{}, fail
	}
	return st, nil
}

func (g *guestStats) callsFor(id string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls[id]
}

func (c *client) overview() gen.AdminOverview {
	c.t.Helper()
	var o gen.AdminOverview
	expect(c.t, "overview", c.do("GET", "/admin/overview", nil, &o), 200)
	return o
}

func machineOf(t *testing.T, o gen.AdminOverview, id string) gen.OverviewMachine {
	t.Helper()
	for _, m := range o.Machines {
		if m.Id == id {
			return m
		}
	}
	t.Fatalf("machine %s is not in the overview", id)
	return gen.OverviewMachine{}
}

func overviewEnv(t *testing.T) (*env, *client, *guestStats) {
	t.Helper()
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	g := newGuestStats()
	e.setStats(g.read)
	return e, e.login("ops", "Ops", "ops@example.com"), g
}

func TestOverviewIsAdminOnly(t *testing.T) {
	e, ops, _ := overviewEnv(t)
	dev := e.login("dev", "Dev", "dev@example.com")
	expect(t, "user", dev.do("GET", "/admin/overview", nil, nil), 403)
	expect(t, "anonymous", e.anon().do("GET", "/admin/overview", nil, nil), 401)
	expect(t, "admin", ops.do("GET", "/admin/overview", nil, nil), 200)
}

func TestOverviewShowsEveryOwnersMachines(t *testing.T) {
	e, ops, g := overviewEnv(t)
	alice := e.login("alice", "Alice", "alice@example.com")
	bob := e.login("bob", "Bob", "bob@example.com")
	a1 := e.fakeAgent(alice, "")
	b1 := e.fakeAgent(bob, "")
	var asleep gen.Agent
	expect(t, "create", bob.do("POST", "/agents", gen.AgentInput{Name: "napper", Harness: ptr("fake"), Cpus: ptr(float32(2)), MemoryMb: ptr(int64(2048))}, &asleep), 201)
	e.sandboxes.Wait()
	expect(t, "sleep", bob.do("POST", "/agents/"+asleep.Id+"/stop", nil, nil), 202)
	e.sandboxes.Wait()
	var app gen.App
	expect(t, "app", alice.do("POST", "/apps", gen.AppInput{Name: "site"}, &app), 201)
	e.sandboxes.Wait()

	// Nobody but the admin owns anything here, and the admin sees it all.
	o := ops.overview()
	if len(o.Machines) != 4 || o.Totals.Machines != 4 {
		t.Fatalf("machines = %d, totals %+v", len(o.Machines), o.Totals)
	}
	m := machineOf(t, o, a1.Id)
	if m.Owner.Email != "alice@example.com" || m.Kind != gen.OverviewMachineKindAgent || m.State != gen.SandboxStateRunning ||
		m.Harness == nil || *m.Harness != "fake" || m.Cpus != a1.Cpus || m.MemoryMb != a1.MemoryMb || m.NodeName == nil {
		t.Fatalf("agent row = %+v", m)
	}
	if m.Usage == nil || m.UsageReason != nil || m.Usage.MemTotalBytes == nil || *m.Usage.MemTotalBytes != 2<<30 ||
		*m.Usage.MemUsedBytes != 1<<30 || *m.Usage.HomeTotalBytes != 10<<30 || *m.Usage.RootUsedBytes != 2<<30 {
		t.Fatalf("usage = %+v (%v)", m.Usage, m.UsageReason)
	}
	if m.Usage.CpuPercent != nil {
		t.Fatal("a CPU share needs two readings")
	}
	if b := machineOf(t, o, b1.Id); b.Owner.Email != "bob@example.com" {
		t.Fatalf("owner = %+v", b.Owner)
	}
	ap := machineOf(t, o, app.Id)
	if ap.Kind != gen.OverviewMachineKindApp || ap.Harness != nil || ap.Usage == nil {
		t.Fatalf("app row = %+v", ap)
	}
	n := machineOf(t, o, asleep.Id)
	if n.State != gen.SandboxStateSleeping || n.Usage != nil || n.UsageReason != nil || n.Cpus != 2 || n.MemoryMb != 2048 {
		t.Fatalf("sleeping row = %+v", n)
	}
	if got := g.callsFor(asleep.Id); got != 0 {
		t.Fatalf("a sleeping machine was asked for stats %d times", got)
	}
	if st := alice.agent(a1.Id).State; st != gen.SandboxStateRunning {
		t.Fatalf("state = %s", st)
	}

	if o.Totals.ByState["running"] != 3 || o.Totals.ByState["sleeping"] != 1 {
		t.Fatalf("byState = %v", o.Totals.ByState)
	}
	if o.Totals.Allocated.Machines != 4 || o.Totals.Running.Machines != 3 || o.Totals.Allocated.MemoryMb != o.Totals.Running.MemoryMb+2048 {
		t.Fatalf("allocation = %+v running %+v", o.Totals.Allocated, o.Totals.Running)
	}
	if len(o.Nodes) != 1 || !o.Nodes[0].Online || o.Nodes[0].Allocated.Machines != 4 || o.Nodes[0].Running.Machines != 3 ||
		o.Nodes[0].Version == nil || (e.edge != nil && o.Nodes[0].Cpus == nil) {
		t.Fatalf("nodes = %+v", o.Nodes)
	}
}

func TestOverviewCPUShareNeedsTwoReadings(t *testing.T) {
	defer api.SetOverviewTiming(time.Second, 20*time.Millisecond)()
	e, ops, _ := overviewEnv(t)
	ag := e.fakeAgent(ops, "")
	if m := machineOf(t, ops.overview(), ag.Id); m.Usage == nil || m.Usage.CpuPercent != nil {
		t.Fatalf("first reading = %+v", m.Usage)
	}
	time.Sleep(50 * time.Millisecond)
	m := machineOf(t, ops.overview(), ag.Id)
	if m.Usage == nil || m.Usage.CpuPercent == nil || *m.Usage.CpuPercent != 50 {
		t.Fatalf("second reading = %+v", m.Usage)
	}
}

func TestOverviewReadingsAreCached(t *testing.T) {
	e, ops, g := overviewEnv(t)
	ag := e.fakeAgent(ops, "")
	for range 3 {
		if machineOf(t, ops.overview(), ag.Id).Usage == nil {
			t.Fatal("no usage")
		}
	}
	if n := g.callsFor(ag.Id); n != 1 {
		t.Fatalf("the guest was asked %d times for three overviews", n)
	}
}

// A machine that never answers costs the page the timeout at most, once;
// everything else is still there.
func TestOverviewHungMachineDoesNotSlowTheResponse(t *testing.T) {
	const timeout = 300 * time.Millisecond
	defer api.SetOverviewTiming(timeout, time.Second)()
	e, ops, g := overviewEnv(t)
	good := e.fakeAgent(ops, "")
	hung := e.fakeAgent(ops, "")
	stuck := e.fakeAgent(ops, "") // ignores its context
	failing := e.fakeAgent(ops, "")
	g.hang[hung.Id] = true
	g.stuck[stuck.Id] = make(chan struct{})
	t.Cleanup(func() { close(g.stuck[stuck.Id]) })
	g.fail[failing.Id] = errors.New("boom")

	start := time.Now()
	o := ops.overview()
	if d := time.Since(start); d > timeout+time.Second {
		t.Fatalf("overview took %s with a %s stats timeout", d, timeout)
	}
	if len(o.Machines) != 4 {
		t.Fatalf("machines = %d", len(o.Machines))
	}
	if m := machineOf(t, o, good.Id); m.Usage == nil {
		t.Fatalf("healthy machine lost its usage: %+v", m)
	}
	for _, id := range []string{hung.Id, stuck.Id} {
		if m := machineOf(t, o, id); m.Usage != nil || m.UsageReason == nil || *m.UsageReason != gen.OverviewMachineUsageReasonTimeout || m.State != gen.SandboxStateRunning {
			t.Fatalf("hung row = %+v", m)
		}
	}
	if m := machineOf(t, o, failing.Id); m.Usage != nil || m.UsageReason == nil || *m.UsageReason != gen.OverviewMachineUsageReasonError {
		t.Fatalf("failing row = %+v", m)
	}

	// The hung machine's failure is cached: the next page is instant and does
	// not pile up another call.
	calls := g.callsFor(stuck.Id)
	start = time.Now()
	ops.overview()
	if d := time.Since(start); d > timeout/2 {
		t.Fatalf("second overview took %s", d)
	}
	if g.callsFor(stuck.Id) != calls {
		t.Fatal("a hung machine was asked again within the cache lifetime")
	}
}

func TestOverviewGuestWithoutUsageStats(t *testing.T) {
	e, ops, g := overviewEnv(t)
	e.setStats(func(context.Context, string) (tunnel.Stats, error) { return tunnel.Stats{Processes: 3}, nil })
	_ = g
	ag := e.fakeAgent(ops, "")
	m := machineOf(t, ops.overview(), ag.Id)
	if m.Usage != nil || m.UsageReason == nil || *m.UsageReason != gen.OverviewMachineUsageReasonUnsupported {
		t.Fatalf("row = %+v", m)
	}
}

// The machines of an offline node are listed as unreachable without asking
// them for anything.
func TestOverviewOfflineNode(t *testing.T) {
	e, ops, g := overviewEnv(t)
	if e.edge == nil {
		t.Skip("needs AGENTHOLD_TEST_NODE=remote")
	}
	ag := e.fakeAgent(ops, "")
	o := ops.overview()
	if len(o.Nodes) != 1 || !o.Nodes[0].Online || o.Nodes[0].Name != "edge" || o.Nodes[0].Version == nil || *o.Nodes[0].Version == "" {
		t.Fatalf("online node = %+v", o.Nodes)
	}
	if m := machineOf(t, o, ag.Id); m.Usage == nil || m.State != gen.SandboxStateRunning {
		t.Fatalf("online row = %+v", m)
	}
	calls := g.callsFor(ag.Id)

	e.edge.down()
	time.Sleep(10 * time.Millisecond)
	start := time.Now()
	o = ops.overview()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("overview of an offline node took %s", d)
	}
	if o.Nodes[0].Version == nil || o.Nodes[0].MemoryTotalBytes == nil || o.Nodes[0].Cpus == nil {
		t.Fatalf("an offline node lost what it reported: %+v", o.Nodes[0])
	}
	if o.Nodes[0].Online || o.Nodes[0].Allocated.Machines != 1 || o.Nodes[0].Running.Machines != 0 || o.Nodes[0].LastSeen == nil {
		t.Fatalf("offline node = %+v", o.Nodes[0])
	}
	m := machineOf(t, o, ag.Id)
	if m.State != gen.SandboxStateUnreachable || m.Usage != nil || m.UsageReason == nil || *m.UsageReason != gen.OverviewMachineUsageReasonNodeOffline {
		t.Fatalf("offline row = %+v", m)
	}
	if o.Totals.ByState["unreachable"] != 1 || g.callsFor(ag.Id) != calls {
		t.Fatalf("totals = %+v, calls %d (was %d)", o.Totals, g.callsFor(ag.Id), calls)
	}
}

// Without any stats override the overview asks the real sandboxd through the
// tunnel; whatever it answers (the in-process one on macOS reports nothing
// beyond processes), the machine keeps its row.
func TestOverviewThroughTheTunnel(t *testing.T) {
	e := newEnvWith(t, func(c *config.Config) { c.Auth.Admins = []string{"ops@example.com"} })
	ops := e.login("ops", "Ops", "ops@example.com")
	ag := e.fakeAgent(ops, "")
	m := machineOf(t, ops.overview(), ag.Id)
	if m.State != gen.SandboxStateRunning || m.Usage == nil && m.UsageReason == nil {
		t.Fatalf("row = %+v", m)
	}
}
