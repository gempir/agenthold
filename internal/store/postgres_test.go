package store

import (
	"context"
	"errors"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/gempir/agenthold/internal/store/db"
)

func TestPostgresURLReopenIsolationAndCleanup(t *testing.T) {
	location := os.Getenv("AGENTHOLD_TEST_POSTGRES")
	if location == "" {
		t.Skip("set AGENTHOLD_TEST_POSTGRES to exercise PostgreSQL-specific connection/cleanup semantics")
	}
	ctx := t.Context()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = s.CreateUser(ctx, db.CreateUserParams{ID: "same", Name: "first schema"}); err != nil {
		t.Fatal(err)
	}
	if err = other.CreateUser(ctx, db.CreateUserParams{ID: "same", Name: "second schema"}); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err = s.DB.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(location)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	reopened, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	user, err := reopened.GetUser(ctx, "same")
	if err != nil || user.Name != "first schema" {
		t.Fatalf("explicit URL did not reopen migrated data: %+v %v", user, err)
	}
	if err = reopened.Snapshot(ctx, "must-not-exist.db"); !errors.Is(err, ErrSnapshotUnsupported) {
		t.Fatalf("Postgres snapshot error: %v", err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	if user, err = other.GetUser(ctx, "same"); err != nil || user.Name != "second schema" {
		t.Fatalf("schemas not isolated: %+v %v", user, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal("repeated close:", err)
	}
	cfg, err := pgx.ParseConfig(location)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	defer admin.Close()
	var exists bool
	if err = admin.QueryRowContext(context.Background(), "SELECT EXISTS(SELECT 1 FROM information_schema.schemata WHERE schema_name=$1)", schema).Scan(&exists); err != nil || exists {
		t.Fatalf("test schema leaked: %v %v", exists, err)
	}
}
