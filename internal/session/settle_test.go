package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gempir/agenthold/internal/audit"
	"github.com/gempir/agenthold/internal/store"
	"github.com/gempir/agenthold/internal/store/db"
)

type settleEnv struct {
	t  *testing.T
	st *store.Store
	as *AutoSettler
}

const day = int64(86400)

func newSettleEnv(t *testing.T) *settleEnv {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, u := range []string{"usr_owner", "usr_guest"} {
		if err := st.CreateUser(ctx, db.CreateUserParams{ID: u, Name: u, CreatedAt: 1, UpdatedAt: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateAgent(ctx, db.CreateAgentParams{ID: "agt_1", OwnerID: "usr_owner", Name: "a", Image: "x", Harness: "fake", Cpus: 1, MemoryMb: 1024, CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return &settleEnv{t: t, st: st, as: &AutoSettler{Store: st, Audit: &audit.Log{Store: st}}}
}

// session creates a session of agt_1 by user, last active at updated.
func (e *settleEnv) session(id, user string, updated int64) {
	e.t.Helper()
	if err := e.st.CreateAgentSession(context.Background(), db.CreateAgentSessionParams{ID: id, AgentID: "agt_1", Title: id, CreatedBy: user, CreatedAt: updated, UpdatedAt: updated}); err != nil {
		e.t.Fatal(err)
	}
}

func (e *settleEnv) settled(id string) db.AgentSession {
	e.t.Helper()
	s, err := e.st.GetAgentSession(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return s
}

func (e *settleEnv) once(now int64) int {
	e.t.Helper()
	n, err := e.as.Once(context.Background(), time.Unix(now, 0))
	if err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *settleEnv) days(user string, d int64) {
	e.t.Helper()
	if err := e.st.SetUserThreadAutoSettleDays(context.Background(), db.SetUserThreadAutoSettleDaysParams{ThreadAutoSettleDays: d, UpdatedAt: 2, ID: user}); err != nil {
		e.t.Fatal(err)
	}
}

func TestSettleAndUnsettle(t *testing.T) {
	e := newSettleEnv(t)
	ctx := context.Background()
	e.session("ses_1", "usr_owner", 100)
	s, changed, err := Settle(ctx, e.st, "ses_1", "usr_guest", time.Unix(200, 0))
	if err != nil || !changed || !s.SettledAt.Valid || s.SettledAt.Int64 != 200 || s.SettledBy != "usr_guest" {
		t.Fatalf("settle: %+v %v %v", s, changed, err)
	}
	if s, changed, err = Settle(ctx, e.st, "ses_1", "auto", time.Unix(300, 0)); err != nil || changed || s.SettledAt.Int64 != 200 || s.SettledBy != "usr_guest" {
		t.Fatalf("settle twice: %+v %v %v", s, changed, err)
	}
	if s, changed, err = Unsettle(ctx, e.st, "ses_1", time.Unix(400, 0)); err != nil || !changed || s.SettledAt.Valid || s.SettledBy != "" || s.UnsettledAt.Int64 != 400 {
		t.Fatalf("unsettle: %+v %v %v", s, changed, err)
	}
	if _, changed, err = Unsettle(ctx, e.st, "ses_1", time.Unix(500, 0)); err != nil || changed {
		t.Fatalf("unsettle twice: %v %v", changed, err)
	}
	if _, _, err = Settle(ctx, e.st, "ses_missing", "usr_owner", time.Unix(1, 0)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	// A new turn unsettles.
	if _, _, err = Settle(ctx, e.st, "ses_1", "usr_owner", time.Unix(600, 0)); err != nil {
		t.Fatal(err)
	}
	if err := e.st.MarkSessionRunning(ctx, db.MarkSessionRunningParams{UpdatedAt: 700, ID: "ses_1"}); err != nil {
		t.Fatal(err)
	}
	if s := e.settled("ses_1"); s.SettledAt.Valid || s.Status != "running" || s.UpdatedAt != 700 {
		t.Fatalf("after a turn started: %+v", s)
	}
}

func TestAutoSettle(t *testing.T) {
	e := newSettleEnv(t)
	ctx := context.Background()
	const t0 = int64(1_000_000)
	e.session("ses_old", "usr_owner", t0)
	e.session("ses_guest", "usr_guest", t0) // started by a shared user: the owner's setting applies
	e.session("ses_new", "usr_owner", t0+2*day)
	e.session("ses_busy", "usr_owner", t0)
	if err := e.st.SetSessionStatus(ctx, db.SetSessionStatusParams{Status: "running", UpdatedAt: t0, ID: "ses_busy"}); err != nil {
		t.Fatal(err)
	}

	if n := e.once(t0 + 3*day - 1); n != 0 {
		t.Fatalf("settled %d before the default 3 days", n)
	}
	// The guest's own setting does not apply to the owner's agent.
	e.days("usr_guest", 1)
	if n := e.once(t0 + 2*day); n != 0 {
		t.Fatalf("guest setting applied: %d", n)
	}
	// 0 = never.
	e.days("usr_owner", 0)
	if n := e.once(t0 + 100*day); n != 0 {
		t.Fatalf("never: %d", n)
	}
	e.days("usr_owner", 3)
	now := t0 + 3*day + 1
	if n := e.once(now); n != 2 {
		t.Fatalf("settled %d, want ses_old and ses_guest", n)
	}
	for id, want := range map[string]bool{"ses_old": true, "ses_guest": true, "ses_new": false, "ses_busy": false} {
		s := e.settled(id)
		if s.SettledAt.Valid != want || (want && (s.SettledBy != SettledByAuto || s.SettledAt.Int64 != now)) {
			t.Fatalf("%s: %+v", id, s)
		}
	}
	if n := e.once(now + 1); n != 0 {
		t.Fatalf("settled again: %d", n)
	}
	// An unsettle by hand counts as activity: no re-settle until the period passed again.
	if _, _, err := Unsettle(ctx, e.st, "ses_old", time.Unix(now+10, 0)); err != nil {
		t.Fatal(err)
	}
	if n := e.once(now + 20); n != 0 {
		t.Fatalf("re-settled right after unsettle: %d", n)
	}
	if n := e.once(now + 10 + 3*day + 1); n != 2 { // ses_old again, and ses_new by now
		t.Fatalf("after the period: %d", n)
	}
	rows, err := e.st.ListAgentAudit(ctx, db.ListAgentAuditParams{AgentID: "agt_1", ID: 1 << 62, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	autos := 0
	for _, r := range rows {
		if r.Action == audit.SessionSettle && r.UserID == "usr_owner" {
			autos++
		}
	}
	if autos != 4 {
		t.Fatalf("audited %d auto-settles, want 4: %+v", autos, rows)
	}
}

// A candidate that saw a turn after it was listed stays active.
func TestAutoSettleRechecks(t *testing.T) {
	e := newSettleEnv(t)
	ctx := context.Background()
	e.session("ses_1", "usr_owner", 0)
	n, err := e.st.AutoSettleSession(ctx, db.AutoSettleSessionParams{ID: "ses_1", Cutoff: 0})
	if err != nil || n != 0 {
		t.Fatalf("settled at the cutoff: %d %v", n, err)
	}
}
