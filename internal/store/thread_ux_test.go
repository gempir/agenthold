package store

import (
	"database/sql"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestThreadUXQueries(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.CreateUser(ctx, db.CreateUserParams{ID: "u1", Name: "Alice", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(ctx, db.CreateUserParams{ID: "u2", Name: "Bob", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgent(ctx, db.CreateAgentParams{
		ID: "ag1", OwnerID: "u1", Name: "alpha", Image: "base", Harness: "fake", Cpus: 1, MemoryMb: 512, CreatedAt: 1, UpdatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateAgentSession(ctx, db.CreateAgentSessionParams{
		ID: "ses1", AgentID: "ag1", Title: "Refactor auth", CreatedBy: "u1", CreatedAt: 10, UpdatedAt: 10,
	}); err != nil {
		t.Fatal(err)
	}

	// Pin / unpin.
	n, err := s.PinSession(ctx, db.PinSessionParams{PinnedAt: sql.NullInt64{Int64: 100, Valid: true}, ID: "ses1"})
	if err != nil || n != 1 {
		t.Fatalf("pin: n=%d err=%v", n, err)
	}
	sess, err := s.GetAgentSession(ctx, "ses1")
	if err != nil || !sess.PinnedAt.Valid || sess.PinnedAt.Int64 != 100 || sess.PermissionMode != "full-access" {
		t.Fatalf("after pin: %+v %v", sess, err)
	}
	if n, err = s.UnpinSession(ctx, "ses1"); err != nil || n != 1 {
		t.Fatalf("unpin: n=%d err=%v", n, err)
	}

	// Snooze / unsnooze + permission mode + parent kind.
	if err := s.SnoozeSession(ctx, db.SnoozeSessionParams{SnoozedUntil: sql.NullInt64{Int64: 500, Valid: true}, ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionPermissionMode(ctx, db.SetSessionPermissionModeParams{PermissionMode: "supervised", UpdatedAt: 11, ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSessionParentKind(ctx, db.SetSessionParentKindParams{ParentKind: "side_chat", ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.GetAgentSession(ctx, "ses1")
	if !sess.SnoozedUntil.Valid || sess.SnoozedUntil.Int64 != 500 || sess.PermissionMode != "supervised" || sess.ParentKind != "side_chat" {
		t.Fatalf("updated session: %+v", sess)
	}
	// MarkSessionRunning wakes snoozed session.
	if err := s.MarkSessionRunning(ctx, db.MarkSessionRunningParams{UpdatedAt: 12, ID: "ses1"}); err != nil {
		t.Fatal(err)
	}
	sess, _ = s.GetAgentSession(ctx, "ses1")
	if sess.SnoozedUntil.Valid {
		t.Fatalf("MarkSessionRunning should clear snoozed_until, got %+v", sess.SnoozedUntil)
	}

	// Queue CRUD + priority ordering (steer DESC, created_at ASC).
	if err := s.EnqueueSessionMessage(ctx, db.EnqueueSessionMessageParams{
		ID: "q1", SessionID: "ses1", UserID: "u1", UserName: "Alice", Text: "first", Steer: 0, CreatedAt: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.EnqueueSessionMessage(ctx, db.EnqueueSessionMessageParams{
		ID: "q2", SessionID: "ses1", UserID: "u1", UserName: "Alice", Text: "urgent", Steer: 1, CreatedAt: 2000,
	}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSessionQueuedMessages(ctx, "ses1")
	if err != nil || len(list) != 2 || list[0].ID != "q2" || list[1].ID != "q1" {
		t.Fatalf("queue order: %+v %v", list, err)
	}
	cnt, err := s.CountSessionQueuedMessages(ctx, "ses1")
	if err != nil || cnt != 2 {
		t.Fatalf("queue count: %d %v", cnt, err)
	}
	if n, err := s.UpdateSessionQueuedMessage(ctx, db.UpdateSessionQueuedMessageParams{Text: "first edited", ID: "q1", SessionID: "ses1"}); err != nil || n != 1 {
		t.Fatalf("update queue: n=%d err=%v", n, err)
	}
	if n, err := s.DeleteSessionQueuedMessage(ctx, db.DeleteSessionQueuedMessageParams{ID: "q2", SessionID: "ses1"}); err != nil || n != 1 {
		t.Fatalf("delete queue: n=%d err=%v", n, err)
	}

	// Rewind delete + search.
	for seq := int64(1); seq <= 3; seq++ {
		if err := s.InsertSessionEvent(ctx, db.InsertSessionEventParams{
			SessionID: "ses1", Seq: seq, At: seq * 1000, Type: "prompt", UserID: "u1", Data: `{"text":"needle-token"}`,
		}); err != nil {
			t.Fatal(err)
		}
	}
	evHits, err := s.SearchUserSessionEvents(ctx, db.SearchUserSessionEventsParams{UserID: "u1", Needle: "needle-token", MaxRows: 10})
	if err != nil || len(evHits) != 3 {
		t.Fatalf("search events u1: len=%d err=%v", len(evHits), err)
	}
	evHitsBob, err := s.SearchUserSessionEvents(ctx, db.SearchUserSessionEventsParams{UserID: "u2", Needle: "needle-token", MaxRows: 10})
	if err != nil || len(evHitsBob) != 0 {
		t.Fatalf("search events u2 should be isolated: len=%d err=%v", len(evHitsBob), err)
	}
	del, err := s.DeleteSessionEventsFrom(ctx, db.DeleteSessionEventsFromParams{SessionID: "ses1", Seq: 2})
	if err != nil || del != 2 {
		t.Fatalf("delete events from 2: del=%d err=%v", del, err)
	}
}
