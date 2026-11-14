package egress

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/netip"
	"sync"
	"time"

	"github.com/gempir/agenthold/internal/secrets"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Directory implements Agents from the database: it maps source addresses
// to agents through their sandbox subnets and resolves policies and secrets.
type Directory struct {
	Store *store.Store
	Box   *secrets.Box
	Log   *slog.Logger

	mu        sync.Mutex
	subnets   map[netip.Prefix]string
	subnetsAt time.Time
	agents    map[string]cachedAgent
}

type cachedAgent struct {
	agent *Agent
	at    time.Time
}

const directoryTTL = 5 * time.Second

// AgentForSource implements Agents.
func (d *Directory) AgentForSource(ctx context.Context, ip netip.Addr) (*Agent, error) {
	agentID, err := d.agentID(ctx, ip)
	if err != nil || agentID == "" {
		return nil, err
	}
	return d.Agent(ctx, agentID)
}

func (d *Directory) agentID(ctx context.Context, ip netip.Addr) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	find := func() string {
		for p, id := range d.subnets {
			if p.Contains(ip) {
				return id
			}
		}
		return ""
	}
	if id := find(); id != "" && time.Since(d.subnetsAt) < directoryTTL {
		return id, nil
	}
	if time.Since(d.subnetsAt) < 500*time.Millisecond {
		return find(), nil
	}
	rows, err := d.Store.ListSandboxSubnets(ctx)
	if err != nil {
		return "", err
	}
	d.subnets = map[netip.Prefix]string{}
	for _, r := range rows {
		if p, err := netip.ParsePrefix(r.Subnet); err == nil {
			d.subnets[p] = r.AgentID
		}
	}
	d.subnetsAt = time.Now()
	return find(), nil
}

// Agent loads an agent's policy and injections (cached briefly).
func (d *Directory) Agent(ctx context.Context, agentID string) (*Agent, error) {
	d.mu.Lock()
	if c, ok := d.agents[agentID]; ok && time.Since(c.at) < directoryTTL {
		d.mu.Unlock()
		return c.agent, nil
	}
	d.mu.Unlock()

	a, err := d.Store.GetAgent(ctx, agentID)
	if err != nil {
		return nil, err
	}
	out := &Agent{ID: agentID, Policy: ParsePolicy(a.NetworkPolicy)}
	rows, err := d.Store.ListAgentSecrets(ctx, agentID)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		inj, err := d.injection(r.Secret)
		if err != nil {
			d.logger().Error("egress: cannot use secret", "secret", r.Secret.ID, "err", err)
			continue
		}
		out.Injections = append(out.Injections, inj)
	}
	d.mu.Lock()
	if d.agents == nil {
		d.agents = map[string]cachedAgent{}
	}
	d.agents[agentID] = cachedAgent{agent: out, at: time.Now()}
	d.mu.Unlock()
	return out, nil
}

func (d *Directory) injection(s db.Secret) (secrets.Injection, error) {
	var rule secrets.Rule
	if err := json.Unmarshal([]byte(s.Rule), &rule); err != nil {
		return secrets.Injection{}, err
	}
	val, err := d.Box.Open(s.Ciphertext, []byte(s.ID))
	if err != nil {
		return secrets.Injection{}, err
	}
	return secrets.Injection{SecretID: s.ID, Rule: rule, Value: string(val), Placeholder: s.Placeholder}, nil
}

// Invalidate drops cached data, after policy, secret or sandbox changes.
func (d *Directory) Invalidate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.agents = nil
	d.subnetsAt = time.Time{}
}

func (d *Directory) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// ParsePolicy decodes a stored policy; empty or invalid means the default.
func ParsePolicy(s string) Policy {
	if s == "" {
		return DefaultPolicy()
	}
	var p Policy
	if err := json.Unmarshal([]byte(s), &p); err != nil || p.Validate() != nil {
		return DefaultPolicy()
	}
	return p
}

// Recorder writes events to the request log in the background.
type Recorder struct {
	Store *store.Store
	// Keep is how many events are kept per agent. Default 5000.
	Keep int
	ch   chan Event
	once sync.Once
	done chan struct{}
}

// Record queues an event; it never blocks (events are dropped under overload).
func (r *Recorder) Record(e Event) {
	r.once.Do(r.start)
	select {
	case r.ch <- e:
	default:
	}
}

func (r *Recorder) start() {
	r.ch = make(chan Event, 4096)
	r.done = make(chan struct{})
	if r.Keep == 0 {
		r.Keep = 5000
	}
	go r.loop()
}

// Flush waits until queued events are written (tests).
func (r *Recorder) Flush() {
	r.once.Do(r.start)
	for len(r.ch) > 0 {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
}

func (r *Recorder) loop() {
	written := map[string]int{}
	for e := range r.ch {
		ctx := context.Background()
		err := r.Store.InsertEgressEvent(ctx, db.InsertEgressEventParams{
			AgentID: e.AgentID, At: e.At.UnixMilli(), Kind: e.Kind, Host: e.Host, Ip: e.IP, Port: int64(e.Port),
			Method: e.Method, Path: truncate(e.Path, 512), Status: int64(e.Status), Allowed: b2i(e.Allowed),
			Reason: truncate(e.Reason, 512), Injected: b2i(e.Injected), BytesUp: e.BytesUp, BytesDown: e.BytesDown,
			DurationMs: e.Duration.Milliseconds(),
		})
		if err != nil {
			continue // the agent may have been deleted meanwhile
		}
		written[e.AgentID]++
		if written[e.AgentID]%200 == 0 {
			_ = r.Store.PruneEgressEvents(ctx, db.PruneEgressEventsParams{AgentID: e.AgentID, Keep: int64(r.Keep)})
		}
	}
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
