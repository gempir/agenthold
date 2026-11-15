package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

// This contract runs unchanged on both engines. AGENTHOLD_TEST_POSTGRES selects
// an isolated PostgreSQL schema for the same API/fake-driver test fixtures.
func TestEngineContract(t *testing.T) {
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	})
	if os.Getenv("AGENTHOLD_TEST_POSTGRES") != "" && s.Engine() != PostgreSQL {
		t.Fatal("Postgres suite silently selected SQLite")
	}
	user := db.CreateUserParams{ID: "owner", Email: "owner@example.com", EmailVerified: 1, Name: "Zoë", CreatedAt: 1, UpdatedAt: 2}
	if err = s.CreateUser(ctx, user); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback requested")
	err = s.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateUser(ctx, db.CreateUserParams{ID: "rolled-back"}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if _, err = s.GetUser(ctx, "rolled-back"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("transaction did not roll back: %v", err)
	}
	err = s.Tx(ctx, func(q *db.Queries) error {
		if err := q.CreateAgent(ctx, db.CreateAgentParams{ID: "agent", OwnerID: user.ID, Name: "persistent agent", Image: "base", Cpus: 0.25, MemoryMb: 512}); err != nil {
			return err
		}
		return q.CreateSandbox(ctx, db.CreateSandboxParams{AgentID: "agent", Driver: "fake", State: "creating"})
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := s.GetAgentWithSandbox(ctx, "agent")
	if err != nil || agent.Agent.Cpus != 0.25 || agent.Agent.MemoryMb != 512 || agent.Sandbox.State != "creating" {
		t.Fatalf("embedded row: %+v %v", agent, err)
	}
	// Compare-and-swap must admit exactly one concurrent lifecycle transition.
	var changed atomic.Int64
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			n, err := s.TransitionSandbox(ctx, db.TransitionSandboxParams{AgentID: "agent", ToState: "starting", FromStates: []string{"creating"}, Now: 17})
			if err != nil {
				t.Error(err)
			}
			changed.Add(n)
		})
	}
	group.Wait()
	if changed.Load() != 1 {
		t.Fatalf("CAS admitted %d transitions", changed.Load())
	}
	for _, from := range [][]string{nil, {}} {
		n, err := s.TransitionSandbox(ctx, db.TransitionSandboxParams{AgentID: "agent", ToState: "running", FromStates: from})
		if err != nil || n != 0 {
			t.Fatalf("empty states: n=%d err=%v", n, err)
		}
	}
	if err = s.CreateAgentSession(ctx, db.CreateAgentSessionParams{ID: "session", AgentID: "agent", CreatedBy: user.ID, Title: "chat"}); err != nil {
		t.Fatal(err)
	}
	if seq, err := s.MaxSessionSeq(ctx, "session"); err != nil || seq != 0 {
		t.Fatalf("empty sequence: %d %v", seq, err)
	}
	const milliseconds int64 = 1844674407370
	if err = s.InsertSessionEvent(ctx, db.InsertSessionEventParams{SessionID: "session", Seq: 1, At: milliseconds, Type: "message", Data: `{"text":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	events, err := s.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: "session", Limit: 10})
	if err != nil || len(events) != 1 || events[0].At != milliseconds {
		t.Fatalf("64-bit timestamp: %+v %v", events, err)
	}
	cipher := []byte{0, 255, 128, '\n', 'x'}
	if err = s.CreateSecret(ctx, db.CreateSecretParams{ID: "secret", OwnerID: user.ID, Name: "TOKEN", Ciphertext: cipher, Rule: "{}", Placeholder: "ph-private"}); err != nil {
		t.Fatal(err)
	}
	secret, err := s.GetSecret(ctx, "secret")
	if err != nil || !bytes.Equal(secret.Ciphertext, cipher) {
		t.Fatalf("binary value: %+v %v", secret, err)
	}
	err = s.CreateSecret(ctx, db.CreateSecretParams{ID: "duplicate", OwnerID: user.ID, Name: "TOKEN", Ciphertext: []byte{}, Rule: "{}", Placeholder: "ph-other"})
	if !IsUniqueViolation(fmt.Errorf("wrapped: %w", err)) {
		t.Fatalf("unique error classification: %v", err)
	}
	err = s.CreateAgent(ctx, db.CreateAgentParams{ID: "bad-owner", OwnerID: "missing", Name: "bad", Image: "base", Cpus: 1, MemoryMb: 512})
	if err == nil || IsUniqueViolation(err) {
		t.Fatalf("foreign key misclassified: %v", err)
	}
	if err = s.CreateAPIToken(ctx, db.CreateAPITokenParams{ID: "token", UserID: user.ID, TokenHash: "hash", ExpiresAt: sql.NullInt64{}}); err != nil {
		t.Fatal(err)
	}
	tokens, err := s.ListAPITokens(ctx, user.ID)
	if err != nil || len(tokens) != 1 || tokens[0].ExpiresAt.Valid || tokens[0].LastUsedAt.Valid {
		t.Fatalf("nullable timestamps: %+v %v", tokens, err)
	}
	if err = s.InsertAudit(ctx, db.InsertAuditParams{UserID: user.ID, At: milliseconds, Action: "contract", Details: "{}"}); err != nil {
		t.Fatal(err)
	}
	audit, err := s.ListUserAudit(ctx, user.ID)
	if err != nil || len(audit) != 1 || audit[0].ID < 1 || audit[0].At != milliseconds {
		t.Fatalf("auto increment audit: %+v %v", audit, err)
	}
	if err = s.DeleteAgent(ctx, "agent"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetAgentSession(ctx, "session"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session cascade: %v", err)
	}
	events, err = s.ListSessionEvents(ctx, db.ListSessionEventsParams{SessionID: "session", Limit: 10})
	if err != nil || events == nil || len(events) != 0 {
		t.Fatalf("empty result contract: %+v %v", events, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.GetUser(cancelled, user.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled query: %v", err)
	}
}

func TestSQLiteURLAndSnapshot(t *testing.T) {
	t.Chdir(t.TempDir())
	encoded := "sqlite:database%20%23%3F.db"
	s, err := Open(t.Context(), encoded)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Engine() != SQLite {
		t.Fatal(s.Engine())
	}
	if _, err = os.Stat("database #?.db"); err != nil {
		t.Fatal(err)
	}
	if err = s.CreateUser(t.Context(), db.CreateUserParams{ID: "saved", Name: "snapshot"}); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(t.TempDir(), "snapshot.db")
	if err = s.Snapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(t.Context(), "sqlite:"+snapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	user, err := copy.GetUser(t.Context(), "saved")
	if err != nil || user.Name != "snapshot" {
		t.Fatalf("snapshot contents: %+v %v", user, err)
	}
	if err = s.Snapshot(t.Context(), snapshot); err == nil {
		t.Fatal("snapshot overwrote existing data")
	}
}

func TestDatabaseLocations(t *testing.T) {
	for _, input := range []string{"", "mysql://localhost/db", "postgres:malformed", "sqlite:", "sqlite://remote/db", "sqlite:file.db?mode=ro"} {
		if _, _, err := parseLocation(input); err == nil {
			t.Errorf("accepted %q", input)
		}
	}
	for _, input := range []string{":memory:", "sqlite::memory:", "relative.db", "sqlite:relative.db", "sqlite:///tmp/db", "postgres://localhost/db", "postgresql:///db?host=/run/postgresql"} {
		if _, _, err := parseLocation(input); err != nil {
			t.Errorf("%s: %v", input, err)
		}
	}
}
