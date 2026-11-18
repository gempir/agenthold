package api

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/api/gen"
	"github.com/gempir/agenthold/internal/apps"
	"github.com/gempir/agenthold/internal/node"
	"github.com/gempir/agenthold/internal/sandbox"
	"github.com/gempir/agenthold/internal/tunnel"
)

// StatsFunc reads the live stats of a running machine from inside its guest.
type StatsFunc func(ctx context.Context, agentID string) (tunnel.Stats, error)

var (
	// overviewStatsTimeout bounds how long the overview waits for any machine.
	overviewStatsTimeout = 1500 * time.Millisecond
	// overviewStatsTTL is how long a reading (or a failure) is reused.
	overviewStatsTTL = 3 * time.Second
)

const (
	// overviewStatsWindow is the longest span between two readings that still
	// yields a CPU percentage.
	overviewStatsWindow = time.Minute
	// overviewMaxFetches bounds the stats calls in flight at once.
	overviewMaxFetches = 32
)

// statsReading is one stats call's result.
type statsReading struct {
	stats tunnel.Stats
	at    time.Time
}

// machineStats is the cached usage of one machine and its fetch in flight.
type machineStats struct {
	mu       sync.Mutex
	prev     *statsReading
	cur      *statsReading
	reason   gen.OverviewMachineUsageReason // why the last fetch failed; "" if it worked
	failedAt time.Time
	inflight chan struct{}
	stuck    bool // a fetch outlived its timeout and has not returned
	used     time.Time
}

// usageCache reads machine stats for the overview: at most one call per
// machine in flight, shared by concurrent requests, reused for a few seconds,
// and never longer than overviewStatsTimeout for a caller.
type usageCache struct {
	fetch func(ctx context.Context, agentID string) (tunnel.Stats, error)
	now   func() time.Time
	slots chan struct{}

	mu sync.Mutex
	m  map[string]*machineStats
}

func newUsageCache(fetch StatsFunc, now func() time.Time) *usageCache {
	return &usageCache{fetch: fetch, now: now, slots: make(chan struct{}, overviewMaxFetches), m: map[string]*machineStats{}}
}

func (c *usageCache) entry(id string) *machineStats {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[id]
	if e == nil {
		e = &machineStats{}
		c.m[id] = e
	}
	e.used = c.now()
	return e
}

// forget drops entries nobody asked about for a while (deleted machines).
func (c *usageCache) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, e := range c.m {
		if c.now().Sub(e.used) > 10*time.Minute {
			delete(c.m, id)
		}
	}
}

// usage returns the machine's usage, or why there is none.
func (c *usageCache) usage(ctx context.Context, id string) (*gen.OverviewUsage, gen.OverviewMachineUsageReason) {
	e := c.entry(id)
	e.mu.Lock()
	now := c.now()
	switch {
	case e.cur != nil && now.Sub(e.cur.at) < overviewStatsTTL && e.reason == "":
		u := e.result()
		e.mu.Unlock()
		return u, ""
	case e.reason != "" && now.Sub(e.failedAt) < overviewStatsTTL:
		reason := e.reason
		e.mu.Unlock()
		return nil, reason
	}
	if e.stuck {
		e.reason, e.failedAt = gen.OverviewMachineUsageReasonTimeout, now
		e.mu.Unlock()
		return nil, gen.OverviewMachineUsageReasonTimeout
	}
	done := e.inflight
	if done == nil {
		done = make(chan struct{})
		e.inflight = done
		go c.run(id, e, done)
	}
	e.mu.Unlock()

	timer := time.NewTimer(overviewStatsTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		return nil, gen.OverviewMachineUsageReasonTimeout
	case <-ctx.Done():
		return nil, gen.OverviewMachineUsageReasonTimeout
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reason != "" {
		return nil, e.reason
	}
	return e.result(), ""
}

// run is the one fetch of e in flight. It outlives the request that started it
// (other requests share it) but not overviewStatsTimeout.
func (c *usageCache) run(id string, e *machineStats, done chan struct{}) {
	defer close(done)
	finish := func(st tunnel.Stats, err error) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.inflight = nil
		at := c.now()
		if err != nil {
			e.reason, e.failedAt = reasonFor(err), at
			e.prev, e.cur = nil, nil
			return
		}
		e.reason = ""
		if st.CPUTotalTicks == 0 && st.MemTotalBytes == 0 && st.HomeTotalBytes == 0 && st.RootTotalBytes == 0 {
			e.reason, e.failedAt = gen.OverviewMachineUsageReasonUnsupported, at
			e.prev, e.cur = nil, nil
			return
		}
		e.prev, e.cur = e.cur, &statsReading{stats: st, at: at}
	}
	ctx, cancel := context.WithTimeout(context.Background(), overviewStatsTimeout)
	defer cancel()
	type result struct {
		st  tunnel.Stats
		err error
	}
	res := make(chan result, 1)
	go func() {
		select {
		case c.slots <- struct{}{}:
			defer func() { <-c.slots }()
		case <-ctx.Done():
			res <- result{err: ctx.Err()}
			return
		}
		st, err := c.fetch(ctx, id)
		res <- result{st, err}
	}()
	select {
	case r := <-res:
		finish(r.st, r.err)
	case <-ctx.Done():
		// A call that ignores its context must not hold the machine's slot in
		// the cache: report the timeout now and let no second call start
		// until the first one returns.
		e.mu.Lock()
		e.stuck = true
		e.mu.Unlock()
		finish(tunnel.Stats{}, ctx.Err())
		go func() {
			<-res
			e.mu.Lock()
			e.stuck = false
			e.mu.Unlock()
		}()
	}
}

func reasonFor(err error) gen.OverviewMachineUsageReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return gen.OverviewMachineUsageReasonTimeout
	case errors.Is(err, tunnel.ErrNotConnected), errors.Is(err, sandbox.ErrUnreachable):
		return gen.OverviewMachineUsageReasonNotConnected
	}
	return gen.OverviewMachineUsageReasonError
}

// result converts the latest reading; e.mu must be held.
func (e *machineStats) result() *gen.OverviewUsage {
	st := e.cur.stats
	out := &gen.OverviewUsage{SampledAt: e.cur.at.UTC()}
	if st.MemTotalBytes > 0 {
		out.MemTotalBytes, out.MemUsedBytes = ptr64(st.MemTotalBytes), ptr64(st.MemUsedBytes)
	}
	if st.HomeTotalBytes > 0 {
		out.HomeTotalBytes, out.HomeUsedBytes = ptr64(st.HomeTotalBytes), ptr64(st.HomeUsedBytes)
	}
	if st.RootTotalBytes > 0 {
		out.RootTotalBytes, out.RootUsedBytes = ptr64(st.RootTotalBytes), ptr64(st.RootUsedBytes)
	}
	// A CPU share needs two readings of the same boot, not too far apart.
	if p := e.prev; p != nil && st.CPUTotalTicks > p.stats.CPUTotalTicks && st.CPUBusyTicks >= p.stats.CPUBusyTicks &&
		e.cur.at.Sub(p.at) <= overviewStatsWindow {
		pct := float64(st.CPUBusyTicks-p.stats.CPUBusyTicks) / float64(st.CPUTotalTicks-p.stats.CPUTotalTicks) * 100
		v := float32(math.Min(pct, 100))
		out.CpuPercent = &v
	}
	return out
}

func ptr64(v uint64) *int64 {
	n := int64(min(v, math.MaxInt64))
	return &n
}

// machineStats reads stats through the node's tunnel to the guest, unless the
// deployment replaced that (tests).
func (a *API) machineStats(ctx context.Context, agentID string) (tunnel.Stats, error) {
	if a.Deps.Stats != nil {
		return a.Deps.Stats(ctx, agentID)
	}
	return node.Client(a.Node, agentID).Stats(ctx)
}

func addAllocation(dst *gen.OverviewAllocation, cpus float64, memoryMB int64) {
	dst.Machines++
	dst.Cpus += float32(cpus)
	dst.MemoryMb += memoryMB
}

// GetAdminOverview implements gen.StrictServerInterface.
func (a *API) GetAdminOverview(ctx context.Context, _ gen.GetAdminOverviewRequestObject) (gen.GetAdminOverviewResponseObject, error) {
	if err := a.requireAdmin(ctx); err != nil {
		return nil, err
	}
	infos, err := a.nodeInfos(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := a.Store.ListAllMachines(ctx)
	if err != nil {
		return nil, err
	}
	a.usage.forget()

	out := gen.AdminOverview{
		GeneratedAt: a.Now().UTC(),
		Nodes:       make([]gen.OverviewNode, 0, len(infos)),
		Machines:    make([]gen.OverviewMachine, len(rows)),
		Totals:      gen.OverviewTotals{ByState: map[string]int{}},
	}
	nodeIdx := map[string]int{}
	for i, info := range infos {
		n := gen.OverviewNode{Id: info.ID, Name: info.Name, Online: info.Connected, Local: info.Local}
		if info.Version != "" {
			n.Version = &info.Version
		}
		if !info.LastSeen.IsZero() {
			seen := info.LastSeen
			n.LastSeen = &seen
		}
		if info.Capacity.CPUs > 0 {
			cpus := info.Capacity.CPUs
			n.Cpus, n.AvailableMemoryBytes, n.FreeDiskBytes = &cpus, ptr64(info.Capacity.MemoryBytes), ptr64(info.Capacity.DiskBytes)
		}
		if info.MemoryTotalBytes > 0 {
			n.MemoryTotalBytes = ptr64(info.MemoryTotalBytes)
		}
		out.Nodes = append(out.Nodes, n)
		nodeIdx[info.ID] = i
	}

	var wg sync.WaitGroup
	for i, r := range rows {
		ag, sb, owner := r.Agent, r.Sandbox, r.User
		info := nodeByID(infos, sb.NodeID)
		m := gen.OverviewMachine{
			Id: ag.ID, Name: ag.Name, Kind: gen.OverviewMachineKind(ag.Kind), IsTemplate: ag.IsTemplate == 1,
			Owner: apiUser(owner), NodeId: &info.ID, NodeName: &info.Name,
			State: gen.SandboxState(sb.State), StateError: sb.Error, StateChangedAt: ts(sb.StateChangedAt),
			Cpus: float32(ag.Cpus), MemoryMb: ag.MemoryMb, CreatedAt: ts(ag.CreatedAt),
		}
		if ag.Kind == apps.KindAgent {
			m.Harness, m.Model = &ag.Harness, &ag.Model
		}
		idleMinutes := int(ag.IdleMinutes)
		m.IdleMinutes = &idleMinutes
		running := sandbox.Phase(sb.State) == sandbox.PhaseRunning
		switch {
		case !info.Connected:
			m.State, running = gen.SandboxStateUnreachable, false
			if sandbox.Phase(sb.State) == sandbox.PhaseRunning {
				reason := gen.OverviewMachineUsageReasonNodeOffline
				m.UsageReason = &reason
			}
		case running && a.Idle != nil:
			if l := a.Idle.Tracker.Last(ag.ID); !l.IsZero() {
				m.LastActivityAt = &l
			}
			if s := a.Idle.SleepsAt(ag.ID, ag.IdleMinutes); !s.IsZero() {
				m.SleepsAt = &s
			}
		}
		out.Machines[i] = m

		out.Totals.Machines++
		out.Totals.ByState[string(m.State)]++
		addAllocation(&out.Totals.Allocated, ag.Cpus, ag.MemoryMb)
		if running {
			addAllocation(&out.Totals.Running, ag.Cpus, ag.MemoryMb)
		}
		if n, ok := nodeIdx[info.ID]; ok {
			addAllocation(&out.Nodes[n].Allocated, ag.Cpus, ag.MemoryMb)
			if running {
				addAllocation(&out.Nodes[n].Running, ag.Cpus, ag.MemoryMb)
			}
		}
		if !running {
			continue
		}
		// Only running machines are asked; sleeping ones are never woken.
		wg.Add(1)
		go func() {
			defer wg.Done()
			u, reason := a.usage.usage(ctx, ag.ID)
			out.Machines[i].Usage = u
			if reason != "" {
				out.Machines[i].UsageReason = &reason
			}
		}()
	}
	wg.Wait()
	return gen.GetAdminOverview200JSONResponse(out), nil
}
