package session

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/gempir/agenthold/internal/store/db"
)

// RewindResult summarizes what Rewind removed and extracted.
type RewindResult struct {
	Seq           int64
	PromptText    string
	DeletedEvents int64
}

// clearPendingPermission removes a pending permission channel when its wait
// ends due to context cancellation.
func (m *Manager) clearPendingPermission(sessionID, requestID string, ch chan acp.PermissionOptionId) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if t := m.turns[sessionID]; t != nil && t.perms[requestID] == ch {
		delete(t.perms, requestID)
	}
}

// SetSessionHarness switches a session's harness without mutating its parent
// agent. If the harness changes from the session's previous effective harness,
// the session's model override is reset and Context Handoff carries history on
// the next turn.
func (m *Manager) SetSessionHarness(ctx context.Context, sessionID, harnessID string) error {
	if m.Running(sessionID) {
		return ErrBusy
	}
	sess, err := m.store.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	agent, err := m.store.GetAgent(ctx, sess.AgentID)
	if err != nil {
		return err
	}
	prevEffective := sess.Harness
	if prevEffective == "" {
		prevEffective = agent.Harness
	}
	// Persist empty string when reverting to the agent's default harness so
	// agent-level defaults apply cleanly again, or the explicit harness id.
	stored := harnessID
	if stored == agent.Harness {
		stored = ""
	}
	if err := m.store.SetSessionHarness(ctx, db.SetSessionHarnessParams{
		Harness: stored,
		ID:      sessionID,
	}); err != nil {
		return err
	}
	if harnessID != prevEffective {
		// Reset per-session model choice since models differ across harnesses.
		if err := m.store.SetSessionModel(ctx, db.SetSessionModelParams{
			Model:        "",
			ModelOptions: "{}",
			ID:           sessionID,
		}); err != nil {
			return err
		}
		// Ensure acp_harness records the previous harness for legacy sessions
		// so the next turn detects harnessSwitched and performs Context Handoff.
		if sess.AcpSessionID != "" && sess.AcpHarness == "" {
			_ = m.store.SetSessionNative(ctx, db.SetSessionNativeParams{
				AcpSessionID: sess.AcpSessionID,
				AcpHarness:   prevEffective,
				ID:           sessionID,
			})
		}
	}
	return nil
}

// SetSessionPermissionMode updates a session's permission mode. If a turn is
// currently running, the new mode takes effect for subsequent permission
// requests immediately.
func (m *Manager) SetSessionPermissionMode(ctx context.Context, sessionID, mode string) error {
	mode = NormalizePermissionMode(mode)
	if err := m.store.SetSessionPermissionMode(ctx, db.SetSessionPermissionModeParams{
		PermissionMode: mode,
		UpdatedAt:      time.Now().Unix(),
		ID:             sessionID,
	}); err != nil {
		return err
	}
	m.mu.Lock()
	if t := m.turns[sessionID]; t != nil && !t.finished {
		t.permissionMode = mode
	}
	m.mu.Unlock()
	return nil
}

// Rewind deletes all session events with seq >= seq, forgets the harness's
// native session so rewound turns are discarded, and marks pending context
// handoff if earlier events remain.
func (m *Manager) Rewind(ctx context.Context, sessionID string, seq int64, userID string) (RewindResult, error) {
	if seq < 1 {
		return RewindResult{}, ErrEventNotFound
	}
	if m.Running(sessionID) {
		return RewindResult{}, ErrBusy
	}
	sess, err := m.store.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return RewindResult{}, ErrNotFound
	}
	if err != nil {
		return RewindResult{}, err
	}
	ev, err := m.store.GetSessionEvent(ctx, db.GetSessionEventParams{
		SessionID: sessionID,
		Seq:       seq,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return RewindResult{}, ErrEventNotFound
	}
	if err != nil {
		return RewindResult{}, err
	}

	var promptText string
	if ev.Type == EventPrompt {
		var p struct {
			Text string `json:"text"`
		}
		if json.Unmarshal([]byte(ev.Data), &p) == nil {
			promptText = p.Text
		}
	}

	deleted, err := m.log.rewind(ctx, sessionID, seq)
	if err != nil {
		return RewindResult{}, err
	}

	// Forget and best-effort close the old harness session so rewound turns are
	// never replayed from the harness's native memory.
	if sess.AcpSessionID != "" {
		m.forgetHarnessSession(sess.AgentID, sess.Harness, sess.AcpHarness, sess.AcpSessionID)
	}
	if err := m.store.SetSessionNative(ctx, db.SetSessionNativeParams{
		AcpSessionID: "",
		AcpHarness:   "",
		ID:           sessionID,
	}); err != nil {
		return RewindResult{}, err
	}

	// If earlier events remain before the rewind point, force Context Handoff
	// on the next prompt so the fresh harness session receives only history < seq.
	remainingSeq, _ := m.store.MaxSessionSeq(ctx, sessionID)
	handoffSeq := int64(0)
	if remainingSeq > 0 {
		handoffSeq = -1
	}
	_ = m.store.SetSessionHandoffSeq(ctx, db.SetSessionHandoffSeqParams{
		HandoffSeq: handoffSeq,
		ID:         sessionID,
	})

	_ = m.log.append(ctx, sessionID, EventNotice, userID, map[string]any{
		"message":   fmt.Sprintf("Rewound conversation to event #%d.", seq),
		"rewoundTo": seq,
	})

	return RewindResult{
		Seq:           seq,
		PromptText:    promptText,
		DeletedEvents: deleted,
	}, nil
}

func (m *Manager) forgetHarnessSession(agentID, sessHarness, acpHarness, acpID string) {
	m.mu.Lock()
	var targets []*harnessConn
	for k, c := range m.conns {
		if k.agentID == agentID && (k.harness == sessHarness || k.harness == acpHarness || c.tracks(acpID)) {
			targets = append(targets, c)
		}
	}
	m.mu.Unlock()
	for _, c := range targets {
		c.forget(acpID)
		if c.proto != nil && !c.closed() {
			cctx, cancel := context.WithTimeout(m.ctx, 3*time.Second)
			_ = c.proto.CloseSession(cctx, acpID)
			cancel()
		}
	}
}

// deliverSideChatHandoff seeds a new side chat with budgeted context
// reconstructed from its parent session's event log.
func (m *Manager) deliverSideChatHandoff(ctx context.Context, conn *harnessConn, sess db.AgentSession, acpID, userText string, notice func(string)) (string, int64) {
	msgs, err := ReconstructHistory(ctx, m.store, sess.OriginSessionID, 0, 0)
	if err != nil {
		m.lg.Warn("side chat: reconstruct parent history failed", "session", sess.ID, "parent", sess.OriginSessionID, "err", err)
		return "", 0
	}
	if len(msgs) == 0 {
		return "", 0
	}

	budget := HandoffBudget(HandoffTokenCap(), userText, DefaultContextWindow)
	sel := SelectHistory(sess.OriginSessionID, "", conn.adapter.ID, msgs, budget)
	if len(sel.Messages) == 0 {
		return "", 0
	}

	injected, ierr := conn.proto.InjectHistory(ctx, acpID, sess.OriginSessionID, sel)
	if ierr != nil {
		m.lg.Warn("side chat: native inject failed, falling back to inline", "session", sess.ID, "err", ierr)
	}
	if injected {
		_ = m.store.SetSessionHandoffSeq(context.WithoutCancel(ctx), db.SetSessionHandoffSeqParams{
			HandoffSeq: sel.CoveredToSeq,
			ID:         sess.ID,
		})
		notice(fmt.Sprintf("Seeded side chat context from parent thread (%d conversation items).", len(sel.Messages)))
		return "", 0
	}

	if IsSlashCommand(userText) {
		_ = m.store.SetSessionHandoffSeq(context.WithoutCancel(ctx), db.SetSessionHandoffSeqParams{
			HandoffSeq: -1,
			ID:         sess.ID,
		})
		return "", 0
	}

	notice(fmt.Sprintf("Seeded side chat context from parent thread (%d conversation items).", len(sel.Messages)))
	return RenderHistory(sess.OriginSessionID, sel), sel.CoveredToSeq
}
