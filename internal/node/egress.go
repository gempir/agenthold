package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/secrets"
)

// AgentConfig contains all the proxy state the controller pushes to a node.
// Values are held only in memory and never written to disk by the node.
type AgentConfig struct {
	ID         string
	Subnet     netip.Prefix
	Injections []secrets.Injection
	Intercepts []Intercept
}

// Intercept describes a login request handled by the controller.
type (
	Intercept struct{ Host, Method, Path, Placeholder string }
	// InterceptRequest identifies an intercepted request and its originating agent.
	InterceptRequest struct {
		AgentID   string
		Index     int
		Intercept Intercept
	}
)

type (
	interceptResult struct{ Handled bool }
	proxyDirectory  struct {
		mu       sync.RWMutex
		agents   []configuredAgent
		requests chan interceptEvent
	}
)

type configuredAgent struct {
	subnet netip.Prefix
	agent  *egress.Agent
}
type interceptEvent struct {
	info   InterceptRequest
	stream net.Conn
}

func (d *proxyDirectory) AgentForSource(ctx context.Context, ip netip.Addr) (*egress.Agent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, a := range d.agents {
		if a.subnet.IsValid() && a.subnet.Contains(ip) {
			return a.agent, nil
		}
	}
	return nil, nil
}

// SetAgentConfigs atomically replaces the proxy rules with an owned snapshot.
func (n *Local) SetAgentConfigs(ctx context.Context, configs []AgentConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Round-trip the value payload, just as a remote transport would, so callers
	// cannot mutate the node's in-memory rules after the push.
	data, err := json.Marshal(configs)
	if err != nil {
		return err
	}
	var snapshot []AgentConfig
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return err
	}
	agents := make([]configuredAgent, 0, len(snapshot))
	for _, cfg := range snapshot {
		cfg := cfg
		a := &egress.Agent{ID: cfg.ID, Injections: append([]secrets.Injection(nil), cfg.Injections...)}
		for i, ic := range cfg.Intercepts {
			i, ic := i, ic
			a.Responders = append(a.Responders, egress.Responder{Host: ic.Host, Handle: func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != ic.Method || r.URL.Path != ic.Path {
					return false
				}
				body, err := io.ReadAll(io.LimitReader(r.Body, 64*1024+1))
				if err != nil {
					return false
				}
				r.Body = io.NopCloser(io.MultiReader(bytes.NewReader(body), r.Body))
				if len(body) > 64*1024 || !strings.Contains(string(body), ic.Placeholder) {
					return false
				}
				if r.Context().Err() != nil {
					return false
				}
				local, remote := net.Pipe()
				defer local.Close()
				select {
				case n.proxy.requests <- interceptEvent{InterceptRequest{AgentID: cfg.ID, Index: i, Intercept: ic}, remote}:
				case <-r.Context().Done():
					remote.Close()
					return false
				}
				done := make(chan struct{})
				defer close(done)
				go func() {
					select {
					case <-r.Context().Done():
						local.Close()
					case <-done:
					}
				}()
				fail := func() bool { http.Error(w, "login refresh unavailable", http.StatusBadGateway); return true }
				if err := r.Write(local); err != nil {
					return fail()
				}
				reader := bufio.NewReader(local)
				var result interceptResult
				line, err := reader.ReadBytes('\n')
				if err != nil || json.Unmarshal(line, &result) != nil {
					return fail()
				}
				if !result.Handled {
					r.Body = io.NopCloser(bytes.NewReader(body))
					return false
				}
				response, err := http.ReadResponse(reader, r)
				if err != nil {
					return fail()
				}
				defer response.Body.Close()
				for k, v := range response.Header {
					w.Header()[k] = v
				}
				w.WriteHeader(response.StatusCode)
				_, _ = io.Copy(w, response.Body)
				return true
			}})
		}
		agents = append(agents, configuredAgent{cfg.Subnet, a})
	}
	n.proxy.mu.Lock()
	n.proxy.agents = agents
	n.proxy.mu.Unlock()
	return nil
}

// NextIntercept receives a login request stream from the proxy.
func (n *Local) NextIntercept(ctx context.Context) (InterceptRequest, io.ReadWriteCloser, error) {
	select {
	case e := <-n.proxy.requests:
		return e.info, e.stream, nil
	case <-ctx.Done():
		return InterceptRequest{}, nil, ctx.Err()
	}
}

// ProxyAgents is the node-owned source attribution table for the local proxy.
func (n *Local) ProxyAgents() egress.Agents { return n.proxy }
