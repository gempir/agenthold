package store

import (
	"path/filepath"
	"testing"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestOpenMigratesAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agenthold.db")
	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser(t.Context(), db.CreateUserParams{ID: "usr_1", Name: "test", CreatedAt: 1, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	u, err := s.GetUser(t.Context(), "usr_1")
	if err != nil || u.Name != "test" {
		t.Fatalf("got %+v, %v", u, err)
	}
}

// Shared agents show up for the shared user; strangers see nothing; users
// are found for sharing by verified email only, case-insensitively.
func TestSharesAndEmailLookup(t *testing.T) {
	s, err := Open(t.Context(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := t.Context()
	for _, u := range []db.CreateUserParams{
		{ID: "alice", Email: "alice@example.com", EmailVerified: 1},
		{ID: "bob", Email: "Bob@Example.com", EmailVerified: 1},
		{ID: "eve", Email: "bob@example.com"}, // unverified
	} {
		if err := s.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateAgent(ctx, db.CreateAgentParams{ID: "ag", OwnerID: "alice", Name: "a", Image: "img", Cpus: 1, MemoryMb: 512}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSandbox(ctx, db.CreateSandboxParams{AgentID: "ag", Driver: "fake", State: "running"}); err != nil {
		t.Fatal(err)
	}
	users, err := s.ListUsersByVerifiedEmail(ctx, "BOB@example.COM")
	if err != nil || len(users) != 1 || users[0].ID != "bob" {
		t.Fatalf("lookup: %+v, %v", users, err)
	}
	if err := s.ShareAgent(ctx, db.ShareAgentParams{AgentID: "ag", UserID: "bob"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ShareAgent(ctx, db.ShareAgentParams{AgentID: "ag", UserID: "bob"}); err != nil {
		t.Fatalf("sharing twice: %v", err)
	}
	for user, want := range map[string]int{"alice": 1, "bob": 1, "eve": 0} {
		rows, err := s.ListUserAgents(ctx, user)
		if err != nil || len(rows) != want {
			t.Errorf("%s sees %d agents (%v), want %d", user, len(rows), err, want)
		}
	}
}
