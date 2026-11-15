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
	"github.com/gempir/agenthold/internal/tools"
)

// Directory implements Agents from the database: it maps source addresses
// to agents through their sandbox subnets and resolves their secrets.
type Directory struct {
	Store *store.Store
	Box   *secrets.Box
	Log   *slog.Logger
	// Logins adds the injections of the account login an agent uses.
	Logins AccountLogins

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

// AccountLogins resolves what the proxy does for an agent's account login.
type AccountLogins interface {
	Egress(ctx context.Context, a db.Agent) ([]secrets.Injection, []Responder, error)
}

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

// Agent loads an agent's injections (cached briefly).
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
	out := &Agent{ID: agentID}
	rows, err := d.Store.ListAgentSecrets(ctx, agentID)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	use := func(s db.Secret) {
		if seen[s.ID] {
			return
		}
		seen[s.ID] = true
		inj, err := d.injection(s)
		if err != nil {
			d.logger().Error("egress: cannot use secret", "secret", s.ID, "err", err)
			return
		}
		out.Injections = append(out.Injections, inj)
	}
	for _, r := range rows {
		use(r.Secret)
	}
	// Secrets referenced by the agent's MCP servers ({{secret:NAME}}) are
	// injected too: their placeholders are in the servers' headers or env.
	if servers, err := tools.AgentServers(ctx, d.Store, agentID); err == nil {
		if refs, err := tools.Secrets(ctx, d.Store, a.OwnerID, servers); err == nil {
			for _, s := range refs {
				use(s)
			}
		}
	}
	if d.Logins != nil {
		injs, resp, err := d.Logins.Egress(ctx, a)
		if err != nil {
			d.logger().Error("egress: account login", "agent", agentID, "err", err)
		}
		out.Injections = append(out.Injections, injs...)
		out.Responders = append(out.Responders, resp...)
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

// Invalidate drops cached data, after secret, tool or sandbox changes.
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
