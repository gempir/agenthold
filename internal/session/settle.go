package session

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

// Settling: a settled thread (session) is done and leaves the active list.
// It stays readable and usable; a new turn unsettles it again.

// SettledByAuto is settled_by for sessions the auto-settle job settled.
const SettledByAuto = "auto"

// SettledByAgent is the settled_by of a session an agent settled with its
// platform tools.
func SettledByAgent(agentID string) string { return "agent:" + agentID }

// DefaultAutoSettleDays is how long a thread may go without activity before it
// settles itself, unless its agent's owner chose otherwise.
const DefaultAutoSettleDays = 3

// MaxAutoSettleDays bounds the setting.
const MaxAutoSettleDays = 365

// SetAudit sets the audit log that records automatic unsettles.
func (m *Manager) SetAudit(l *audit.Log) { m.audit = l }

// Settle settles a session for by (a user id, SettledByAgent or
// SettledByAuto). changed is false when it was settled already.
func Settle(ctx context.Context, st *store.Store, sessionID, by string, now time.Time) (s db.AgentSession, changed bool, err error) {
	n, err := st.SettleSession(ctx, db.SettleSessionParams{SettledAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, SettledBy: by, ID: sessionID})
	if err != nil {
		return s, false, err
	}
	s, err = st.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return s, n > 0, err
}

// Unsettle makes a settled session active again. The auto-settle period
// starts over from now. changed is false when it was active already.
func Unsettle(ctx context.Context, st *store.Store, sessionID string, now time.Time) (s db.AgentSession, changed bool, err error) {
	n, err := st.UnsettleSession(ctx, db.UnsettleSessionParams{UnsettledAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: sessionID})
	if err != nil {
		return s, false, err
	}
	s, err = st.GetAgentSession(ctx, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return s, n > 0, err
}

// recordUnsettledByActivity audits that a prompt unsettled sess.
func (m *Manager) recordUnsettledByActivity(ctx context.Context, sess db.AgentSession, userID string, opts PromptOpts) {
	details := map[string]any{"reason": "activity", "session": sess.Title}
	if opts.Origin != nil {
		details["fromAgent"], details["fromAgentId"] = opts.Origin["agentName"], opts.Origin["agentId"]
	}
	m.audit.Record(ctx, audit.Entry{AgentID: sess.AgentID, UserID: userID, Action: audit.SessionUnsettle, Target: sess.ID, Details: details})
}

// AutoSettler settles the threads that saw no activity for as many days as
// their agent's owner chose (users.thread_auto_settle_days; 0 = never). The
// owner's setting applies to every thread of the agent, whoever started it,
// so that all viewers of a shared agent see the same state.
//
// It is a periodic job rather than computed on read so that the state is the
// same everywhere it is read (the API, the platform tools, Slack), each
// settle is audited once, and reads stay simple indexed queries.
type AutoSettler struct {
	Store *store.Store
	Audit *audit.Log
	Log   *slog.Logger
	// Every is how often it looks. Default an hour: settling is measured in days.
	Every time.Duration
}

// Run settles at start and then every Every until ctx ends.
func (a *AutoSettler) Run(ctx context.Context) {
	every := a.Every
	if every <= 0 {
		every = time.Hour
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if _, err := a.Once(ctx, time.Now()); err != nil && ctx.Err() == nil {
			a.logger().Warn("auto-settle threads", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once settles what is due at now and returns how many sessions it settled.
func (a *AutoSettler) Once(ctx context.Context, now time.Time) (int, error) {
	rows, err := a.Store.ListAutoSettleCandidates(ctx, now.Unix())
	if err != nil {
		return 0, err
	}
	settled := 0
	for _, r := range rows {
		// Re-checked in the update: a prompt since the listing keeps it active.
		n, err := a.Store.AutoSettleSession(ctx, db.AutoSettleSessionParams{
			SettledAt: sql.NullInt64{Int64: now.Unix(), Valid: true}, ID: r.ID,
			Cutoff: now.Unix() - r.ThreadAutoSettleDays*86400,
		})
		if err != nil {
			return settled, err
		}
		if n == 0 {
			continue
		}
		settled++
		a.Audit.Record(ctx, audit.Entry{
			AgentID: r.AgentID, UserID: r.OwnerID, Action: audit.SessionSettle, Target: r.ID,
			Details: map[string]any{"reason": "auto", "afterDays": r.ThreadAutoSettleDays, "session": r.Title},
		})
	}
	if settled > 0 {
		a.logger().Info("auto-settled idle threads", "count", settled)
	}
	return settled, nil
}

func (a *AutoSettler) logger() *slog.Logger {
	if a.Log != nil {
		return a.Log
	}
	return slog.Default()
}
