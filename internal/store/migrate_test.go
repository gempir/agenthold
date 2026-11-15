package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

// Agents signed in inside their sandbox before account logins existed keep
// using that login; the others use their owner's account logins.
func TestMigrationKeepsSandboxLogins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	sqlDB, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, mustSub(migrations, "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(t.Context(), 9); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users (id, created_at, updated_at) VALUES ('u1', 1, 1)`,
		`INSERT INTO workspaces (id, name, created_at, updated_at) VALUES ('w1', 'w', 1, 1)`,
		`INSERT INTO agents (id, workspace_id, owner_id, name, image, cpus, memory_mb, created_at, updated_at, credentials_in_sandbox) VALUES ('signed-in', 'w1', 'u1', 'a', 'img', 1, 512, 1, 1, 1)`,
		`INSERT INTO agents (id, workspace_id, owner_id, name, image, cpus, memory_mb, created_at, updated_at) VALUES ('plain', 'w1', 'u1', 'b', 'img', 1, 512, 1, 1)`,
	} {
		if _, err := sqlDB.ExecContext(t.Context(), q); err != nil {
			t.Fatal(err)
		}
	}
	_ = sqlDB.Close()

	s, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for id, want := range map[string]string{"signed-in": "", "plain": "u1"} {
		a, err := s.GetAgent(t.Context(), id)
		if err != nil || a.LoginUserID != want {
			t.Fatalf("%s: login user %q, %v", id, a.LoginUserID, err)
		}
	}
}
