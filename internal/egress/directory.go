package egress

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
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
	// Git adds the injections of the agent owner's git credentials. Secrets
	// attached to the agent win: a git credential for a host one of them
	// already covers is left out.
	Git GitCredentials
	// OnInvalidate schedules a push of the updated controller configuration.
	OnInvalidate func()

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

// GitCredentials resolves the account-level git credentials of an agent's owner.
type GitCredentials interface {
	Egress(ctx context.Context, a db.Agent) ([]secrets.Injection, error)
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
	// Org-shared MCP tool secrets ({{org_secret}}) are injected as placeholder
	// replacements for their allowed secret_hosts only; real values never enter
	// the sandbox.
	if owner, err := d.Store.GetUser(ctx, a.OwnerID); err == nil {
		if orgInjs, err := tools.OrgToolInjections(ctx, d.Store, d.Box, owner); err == nil {
			out.Injections = append(out.Injections, orgInjs...)
		} else {
			d.logger().Error("egress: org tool secrets", "agent", agentID, "err", err)
		}
	}
	if d.Git != nil {
		injs, err := d.Git.Egress(ctx, a)
		if err != nil {
			d.logger().Error("egress: git credentials", "agent", agentID, "err", err)
		}
		explicit := out.Injections
		for _, inj := range injs {
			if !coveredHosts(explicit, inj.Rule.Hosts) {
				out.Injections = append(out.Injections, inj)
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
	if !coveredHosts(out.Injections, []string{githubAPIHost}) {
		out.Responders = append(out.Responders, Responder{Host: githubAPIHost, Handle: answerUnconnectedGitHub})
	}
	d.mu.Lock()
	if d.agents == nil {
		d.agents = map[string]cachedAgent{}
	}
	d.agents[agentID] = cachedAgent{agent: out, at: time.Now()}
	d.mu.Unlock()
	return out, nil
}

const githubAPIHost = "api.github.com"

// answerUnconnectedGitHub answers GitHub API requests that carry the GitHub
// CLI's token marker when nothing would authenticate them: GitHub would only
// say "Bad credentials". gh prints the message.
func answerUnconnectedGitHub(w http.ResponseWriter, r *http.Request) bool {
	if !strings.Contains(r.Header.Get("Authorization"), GitHubTokenMarker) {
		return false
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"message": "agenthold: this agent's owner has not connected a GitHub account. " +
			"Connect one in agenthold under Settings → Git (or add a github.com token there), then retry. " +
			"The token is added to requests by the egress proxy and never enters the sandbox.",
		"documentation_url": "https://agenthold.dev/git/#github-cli",
	})
	return true
}

// coveredHosts reports whether one of injs (header-adding secrets) already
// applies to one of hosts.
func coveredHosts(injs []secrets.Injection, hosts []string) bool {
	for _, inj := range injs {
		if inj.HeaderOnly || inj.Rule.Header == "" {
			continue
		}
		for _, h := range hosts {
			if inj.Rule.MatchHost(h) {
				return true
			}
		}
	}
	return false
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
	d.agents = nil
	d.subnetsAt = time.Time{}
	d.mu.Unlock()
	if d.OnInvalidate != nil {
		d.OnInvalidate()
	}
}

func (d *Directory) logger() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}
