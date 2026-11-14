// Package store owns the SQLite database: connection, migrations and queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // cgo-free SQLite driver

	"github.com/gempir/agenthold/internal/store/db"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store wraps the database handle and the generated queries.
type Store struct {
	DB *sql.DB
	*db.Queries
}

// Open opens (and creates) the SQLite database at path and applies all migrations.
// Use ":memory:" for a private in-memory database (tests).
func Open(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	dsn := "file:" + path + "?" + q.Encode()

	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		// Every connection to :memory: is a separate database.
		sqlDB.SetMaxOpenConns(1)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := migrate(ctx, sqlDB); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	return &Store{DB: sqlDB, Queries: db.New(sqlDB)}, nil
}

func migrate(ctx context.Context, sqlDB *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, mustSub(migrations, "migrations"))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

// Tx runs fn inside a transaction and commits if fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(s.WithTx(tx)); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

// RenameAgentSessionTitle sets a session's title.
func (s *Store) RenameAgentSessionTitle(ctx context.Context, id, title string) error {
	return s.RenameSession(ctx, db.RenameSessionParams{Title: title, UpdatedAt: time.Now().Unix(), ID: id})
}
