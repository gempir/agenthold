package session

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store/db"
)

// A harness describes the options of its current model only (Claude Code,
// Codex and OpenCode all do): the reasoning effort levels and fast mode of
// the other models are not in any report. The catalog learns a model's
// options when it is current. The probe learns the others without a turn: it
// starts a throwaway session, switches it through the models that have no
// options recorded yet and records what each answer reports. No prompt is
// sent.
//
// It only runs for adapters that declare it side-effect-free
// (harness.Adapter.ProbeModels): a throwaway session must not be stored,
// run hooks or start MCP servers. None of the harnesses in the image
// qualifies today, so their models show provisional effort levels (the ones
// other models reported) until they have been used.

const (
	// probeLimit bounds the models one probe describes (OpenCode can list
	// hundreds); what is left is probed the next time the harness is idle.
	probeLimit = 60
	// probeIdle is how long the harness must have been idle before probing,
	// so the probe does not compete with a turn that follows a turn.
	probeIdle = 2 * time.Second
	// probeRetry is how long a model that was tried is left alone, whatever
	// came of it (answered with no options, refused, not listed).
	probeRetry = 24 * time.Hour
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

// probeLater schedules a probe of conn's models, at most one at a time per
// connection.
func (m *Manager) probeLater(conn *harnessConn) {
	if !conn.adapter.ProbeModels {
		return
	}
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
		if err := m.probeConn(m.ctx, conn); err != nil {
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
// options the catalog does not know yet (see probeConn).
func (m *Manager) ProbeModels(ctx context.Context, agentID string) error {
	m.mu.Lock()
	conn := m.conns[agentID]
	m.mu.Unlock()
	if conn == nil {
		return nil
	}
	return m.probeConn(ctx, conn)
}

// probeConn describes the models of conn's harness. It does nothing (and
// returns nil) when the adapter does not allow probing, a turn is using the
// harness, there is nothing to find out, or the harness encodes effort in
// model ids (the catalog knows those without asking). Every model it tries is
// remembered in the catalog, whatever comes of it, and left alone for
// probeRetry.
func (m *Manager) probeConn(ctx context.Context, conn *harnessConn) error {
	if !conn.adapter.ProbeModels || conn.closed() || m.busy(conn) {
		return nil
	}
	agentID := conn.agentID
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
		if _, known := cat.ByModel[v.Value]; known {
			continue
		}
		if at, tried := cat.Probed[v.Value]; tried && time.Since(time.Unix(at, 0)) < probeRetry {
			continue
		}
		todo = append(todo, v.Value)
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

	var tried []string
	defer func() { m.markProbed(conn, tried) }()

	sctx, scancel := context.WithTimeout(ctx, setOptionTimeout)
	res, err := conn.acp.NewSession(sctx, acp.NewSessionRequest{Cwd: HomeDir, McpServers: []acp.McpServer{}})
	scancel()
	if err != nil {
		tried = todo // not tried again before probeRetry
		return fmt.Errorf("probe session: %w", err)
	}
	acpID := string(res.SessionId)
	conn.setProbe(acpID, true)
	defer func() {
		conn.setProbe(acpID, false)
		conn.forget(acpID)
		if conn.init.AgentCapabilities.SessionCapabilities.Close != nil && !conn.closed() {
			cctx, ccancel := context.WithTimeout(context.WithoutCancel(ctx), setOptionTimeout)
			defer ccancel()
			_, _ = conn.acp.CloseSession(cctx, acp.CloseSessionRequest{SessionId: res.SessionId})
		}
	}()
	// The probe session's own options are only used to find the model option
	// the harness sets: they are not recorded as a fresh session.
	opts := fromACP(res.ConfigOptions)
	conn.mu.Lock()
	conn.config[acpID] = opts
	conn.mu.Unlock()
	raw, ok := modelOption(opts)
	if !ok {
		tried = todo
		return nil
	}
	for _, id := range todo {
		if ctx.Err() != nil || conn.closed() || m.busy(conn) {
			return nil // what is left is probed next time
		}
		tried = append(tried, id)
		if !raw.Has(id) {
			continue
		}
		if err := m.setOption(ctx, conn, acpID, raw, id); err != nil {
			if ctx.Err() != nil || conn.closed() {
				tried = tried[:len(tried)-1] // not answered: not tried
				return nil
			}
			m.lg.Debug("model probe: model refused", "agent", agentID, "model", id, "err", refusal(err))
		}
	}
	return nil
}

// markProbed records that the models were tried, so they are not tried again
// for probeRetry whatever the outcome was.
func (m *Manager) markProbed(conn *harnessConn, ids []string) {
	if len(ids) == 0 {
		return
	}
	m.catMu.Lock()
	defer m.catMu.Unlock()
	ctx := context.WithoutCancel(m.ctx)
	row, err := m.store.GetModelCatalog(ctx, conn.agentID)
	if err != nil || row.Harness != conn.adapter.ID {
		return
	}
	var c Catalog
	if json.Unmarshal([]byte(row.Data), &c) != nil {
		return
	}
	if c.Probed == nil {
		c.Probed = map[string]int64{}
	}
	now := time.Now().Unix()
	for _, id := range ids {
		if _, known := c.ByModel[id]; !known && len(c.Probed) < maxCatalogModel {
			c.Probed[id] = now
		}
	}
	b, _ := json.Marshal(c)
	if err := m.store.UpsertModelCatalog(ctx, db.UpsertModelCatalogParams{
		AgentID: conn.agentID, Harness: conn.adapter.ID, Data: string(b), SeenAt: row.SeenAt,
	}); err != nil {
		m.lg.Warn("store probed models", "agent", conn.agentID, "err", err)
	}
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
