package session

import (
	"context"
	"fmt"
	"time"

	acp "github.com/coder/acp-go-sdk"
)

// A harness describes the options of its current model only (Claude Code,
// Codex and OpenCode all do): the reasoning effort levels and fast mode of
// the other models are not in any report. The catalog learns a model's
// options when it is current, so the picker would show them only for models
// the agent has already used. The probe closes that gap: it starts a
// throwaway session, switches it through the models that have no options
// recorded yet and records what each answer reports. No prompt is sent, so
// it costs no tokens and the agent's conversations are untouched.

const (
	// probeLimit bounds the models one probe describes (OpenCode can list
	// hundreds); what is left is probed the next time the harness is idle.
	probeLimit = 60
	// probeIdle is how long the harness must have been idle before probing,
	// so the probe does not compete with a turn that follows a turn.
	probeIdle = 2 * time.Second
)

// probeTimeout bounds one probe.
var probeTimeout = 3 * time.Minute

// SetModelProbe turns the probe on: after a turn, once the harness is idle,
// the models whose options are unknown are described (see above). It is off
// by default so tests that script the harness's requests stay deterministic.
func (m *Manager) SetModelProbe(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.probe = on
}

// probeLater schedules a probe of conn's models, at most one per connection
// at a time (and only one after another when the previous one found
// something to do).
func (m *Manager) probeLater(conn *harnessConn) {
	m.mu.Lock()
	if !m.probe || m.closing || conn.closed() || !conn.probing.CompareAndSwap(false, true) {
		m.mu.Unlock()
		return
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer conn.probing.Store(false)
		select {
		case <-time.After(probeIdle):
		case <-conn.done:
			return
		case <-m.ctx.Done():
			return
		}
		if err := m.ProbeModels(m.ctx, conn.agentID); err != nil {
			m.lg.Debug("model probe", "agent", conn.agentID, "err", err)
		}
	}()
}

// busy reports whether a turn uses the connection.
func (m *Manager) busy(c *harnessConn) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inUse(c)
}

// ProbeModels describes the models of the agent's running harness whose
// options the catalog does not know yet. It does nothing (and returns nil)
// when the harness is not running, a turn is using it, there is nothing to
// find out or the harness encodes effort in model ids (the catalog knows
// those without asking).
func (m *Manager) ProbeModels(ctx context.Context, agentID string) error {
	m.mu.Lock()
	conn := m.conns[agentID]
	m.mu.Unlock()
	if conn == nil || conn.closed() || m.busy(conn) {
		return nil
	}
	cat, ok, err := m.Catalog(ctx, agentID)
	if err != nil || !ok || cat.Harness != conn.adapter.ID {
		return err
	}
	mo, ok := cat.ModelOption()
	if !ok {
		return nil
	}
	if raw, ok := conn.rawModelOption(); ok {
		if _, split := splitVariants(raw); split {
			return nil
		}
	}
	var todo []string
	for _, v := range mo.Values {
		if _, known := cat.ByModel[v.Value]; !known && !conn.gaveUpOn("probe:"+v.Value) {
			todo = append(todo, v.Value)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	if len(todo) > probeLimit {
		todo = todo[:probeLimit]
	}

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	go func() {
		select {
		case <-conn.done:
			cancel()
		case <-ctx.Done():
		}
	}()

	var res acp.NewSessionResponse
	sctx, scancel := context.WithTimeout(ctx, setOptionTimeout)
	res, err = conn.acp.NewSession(sctx, acp.NewSessionRequest{Cwd: HomeDir, McpServers: []acp.McpServer{}})
	scancel()
	if err != nil {
		return fmt.Errorf("probe session: %w", err)
	}
	acpID := string(res.SessionId)
	defer func() {
		conn.forget(acpID)
		if conn.init.AgentCapabilities.SessionCapabilities.Close != nil && !conn.closed() {
			cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), setOptionTimeout)
			defer ccancel()
			_, _ = conn.acp.CloseSession(cctx, acp.CloseSessionRequest{SessionId: res.SessionId})
		}
	}()
	// The probe session's own options are the starting point: not recorded as
	// a fresh session (its values are no more the harness's defaults than the
	// first session's), only used to find the model option the harness sets.
	opts := fromACP(res.ConfigOptions)
	conn.mu.Lock()
	conn.config[acpID] = opts
	conn.mu.Unlock()
	raw, ok := modelOption(opts)
	if !ok {
		return nil
	}
	for _, id := range todo {
		if ctx.Err() != nil || conn.closed() || m.busy(conn) {
			return nil // what is left is probed next time
		}
		if !raw.Has(id) {
			continue
		}
		if err := m.setOption(ctx, conn, acpID, raw, id); err != nil {
			if ctx.Err() != nil || conn.closed() {
				return nil
			}
			conn.giveUpOn("probe:" + id)
			m.lg.Debug("model probe: model refused", "agent", agentID, "model", id, "err", refusal(err))
		}
	}
	return nil
}

// rawModelOption is the model option of any of the connection's sessions, as
// the harness reported it.
func (c *harnessConn) rawModelOption() (ConfigOption, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, opts := range c.config {
		if mo, ok := modelOption(opts); ok {
			return mo, true
		}
	}
	return ConfigOption{}, false
}
