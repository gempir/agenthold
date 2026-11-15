package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"sync"

	"github.com/gempir/agenthold/internal/egress"
	"github.com/gempir/agenthold/internal/store"
)

// ConfigPusher resolves secrets in the controller and pushes value-only proxy
// configuration to the node. Sync serializes concurrent updates.
type ConfigPusher struct {
	Store     *store.Store
	Directory *egress.Directory
	Node      Node
	mu        sync.Mutex
}

// Sync replaces the node proxy configuration with a fresh controller snapshot.
func (p *ConfigPusher) Sync(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows, err := p.Store.ListSandboxes(ctx)
	if err != nil {
		return err
	}
	configs := make([]AgentConfig, 0, len(rows))
	for _, sb := range rows {
		a, err := p.Directory.Agent(ctx, sb.AgentID)
		if err != nil {
			return fmt.Errorf("resolve proxy config for %s: %w", sb.AgentID, err)
		}
		subnet, _ := netip.ParsePrefix(sb.Subnet)
		cfg := AgentConfig{ID: sb.AgentID, Subnet: subnet, Injections: a.Injections}
		for _, r := range a.Responders {
			cfg.Intercepts = append(cfg.Intercepts, Intercept{Host: r.Host, Method: r.Method, Path: r.Path, Placeholder: r.Placeholder})
		}
		configs = append(configs, cfg)
	}
	return p.Node.SetAgentConfigs(ctx, configs)
}

// ServeIntercepts handles node-originated login refresh requests until canceled.
func (p *ConfigPusher) ServeIntercepts(ctx context.Context) error {
	for {
		info, stream, err := p.Node.NextIntercept(ctx)
		if err != nil {
			return err
		}
		if stream == nil {
			return fmt.Errorf("node returned a nil intercept stream")
		}
		go p.intercept(ctx, info, stream)
	}
}

func (p *ConfigPusher) intercept(ctx context.Context, info InterceptRequest, stream io.ReadWriteCloser) {
	defer stream.Close()
	req, err := http.ReadRequest(bufio.NewReader(stream))
	if err != nil {
		return
	}
	req = req.WithContext(ctx)
	body, err := io.ReadAll(io.LimitReader(req.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	a, err := p.Directory.Agent(ctx, info.AgentID)
	if err != nil || info.Index < 0 || info.Index >= len(a.Responders) {
		return
	}
	r := a.Responders[info.Index]
	if r.Host != info.Intercept.Host || r.Method != info.Intercept.Method || r.Path != info.Intercept.Path || r.Placeholder != info.Intercept.Placeholder {
		return
	}
	recorder := httptest.NewRecorder()
	handled := r.Handle(recorder, req)
	if json.NewEncoder(stream).Encode(interceptResult{Handled: handled}) != nil || !handled {
		return
	}
	response := recorder.Result()
	defer response.Body.Close()
	_ = response.Write(stream)
}
