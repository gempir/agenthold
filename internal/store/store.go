// Package store owns database connections, migrations and the engine-neutral
// query contract. SQLite is the default; PostgreSQL uses the native pgx driver.
package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // cgo-free SQLite driver

	"github.com/gempir/agenthold/internal/store/db"
	"github.com/gempir/agenthold/internal/store/postgres"
	"github.com/gempir/agenthold/internal/store/sqlite"
)

//go:embed migrations/sqlite/*.sql migrations/postgres/*.sql
var migrations embed.FS

//go:generate go run ./cmd/adaptergen -root ../..

// Engine identifies the database dialect.
type Engine string

// Supported database engines.
const (
	SQLite     Engine = "sqlite"
	PostgreSQL Engine = "postgres"
)

// ErrSnapshotUnsupported identifies databases managed with external backup tools.
var ErrSnapshotUnsupported = errors.New("snapshot backup is only supported for SQLite; use PostgreSQL backup tools")

// Store exposes canonical queries independently of the underlying sqlc engine.
// DB is available for operational queries; application queries belong in sqlc.
type Store struct {
	DB *sql.DB
	*db.Queries
	engine    Engine
	cleanup   func(context.Context) error
	closeOnce sync.Once
	closeErr  error
}

// Open opens a SQLite path, sqlite:<path>, or postgres[ql]:// URL and migrates
// the database. Bare :memory: creates a private SQLite database. When the
// explicit AGENTHOLD_TEST_POSTGRES environment variable is set, bare :memory:
// instead creates an isolated PostgreSQL schema, dropped on Close. Explicit
// sqlite::memory: always selects SQLite (for engine-specific tests).
func Open(ctx context.Context, location string) (*Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if location == ":memory:" {
		if postgresURL := os.Getenv("AGENTHOLD_TEST_POSTGRES"); postgresURL != "" {
			return openPostgres(ctx, postgresURL, true)
		}
	}
	engine, path, err := parseLocation(location)
	if err != nil {
		return nil, err
	}
	if engine == PostgreSQL {
		return openPostgres(ctx, path, false)
	}
	return openSQLite(ctx, path)
}

func parseLocation(location string) (Engine, string, error) {
	if location == "" {
		return "", "", errors.New("database location must not be empty")
	}
	if strings.HasPrefix(location, "postgres://") || strings.HasPrefix(location, "postgresql://") {
		return PostgreSQL, location, nil
	}
	if strings.HasPrefix(location, "sqlite:") {
		u, err := url.Parse(location)
		if err != nil {
			return "", "", errors.New("invalid SQLite URL")
		}
		if u.Host != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", "", errors.New("SQLite URL must be sqlite:<path> without query parameters")
		}
		path := u.Path
		if u.Opaque != "" {
			path, err = url.PathUnescape(u.Opaque)
			if err != nil {
				return "", "", errors.New("invalid SQLite path encoding")
			}
		}
		if path == "" {
			return "", "", errors.New("SQLite URL requires a file path or in-memory database name")
		}
		return SQLite, path, nil
	}
	// Paths remain convenient for tests and internal callers. Explicit URLs
	// select an engine; unsupported URL schemes must never create local files.
	if strings.Contains(location, ":") {
		if u, err := url.Parse(location); err != nil || u.Scheme != "" {
			if location != ":memory:" {
				return "", "", errors.New("unsupported database URL scheme (want sqlite, postgres, or postgresql)")
			}
		}
	}
	return SQLite, location, nil
}

func openSQLite(ctx context.Context, path string) (*Store, error) {
	q := url.Values{}
	for _, p := range []string{"busy_timeout(10000)", "journal_mode(WAL)", "foreign_keys(1)", "synchronous(NORMAL)"} {
		q.Add("_pragma", p)
	}
	q.Set("_txlock", "immediate")
	// Use URL path encoding so '#' and '?' in filenames cannot become URI
	// options and disable the connection's foreign-key or durability settings.
	if path != ":memory:" {
		var err error
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
	}
	fileURL := url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}
	if path == ":memory:" {
		fileURL = url.URL{Scheme: "file", Opaque: ":memory:", RawQuery: q.Encode()}
	}
	sqlDB, err := sql.Open("sqlite", fileURL.String())
	if err != nil {
		return nil, err
	}
	if path == ":memory:" {
		sqlDB.SetMaxOpenConns(1)
	}
	if err = sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, fmt.Errorf("open SQLite database: %w", err)
	}
	if err = migrate(ctx, sqlDB, SQLite); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}
	qdb := sqlite.New(sqlDB)
	return &Store{DB: sqlDB, engine: SQLite, Queries: db.New(qdb, func(tx *sql.Tx) db.Querier { return qdb.WithTx(tx) })}, nil
}

func openPostgres(ctx context.Context, location string, isolated bool) (*Store, error) {
	if !strings.HasPrefix(location, "postgres://") && !strings.HasPrefix(location, "postgresql://") {
		return nil, errors.New("PostgreSQL URL must begin with postgres:// or postgresql://")
	}
	cfg, err := pgx.ParseConfig(location)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection URL")
	}
	var cleanup func(context.Context) error
	if isolated {
		var nonce [12]byte
		if _, err = rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		schema := "agenthold_test_" + hex.EncodeToString(nonce[:])
		admin := stdlib.OpenDB(*cfg)
		admin.SetMaxOpenConns(1)
		quoted := pgx.Identifier{schema}.Sanitize()
		if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
			_ = admin.Close()
			return nil, fmt.Errorf("create isolated PostgreSQL schema: %w", err)
		}
		cleanup = func(ctx context.Context) error {
			_, err := admin.ExecContext(ctx, "DROP SCHEMA "+quoted+" CASCADE")
			return errors.Join(err, admin.Close())
		}
		cfg = cfg.Copy()
		cfg.RuntimeParams["search_path"] = quoted
	}
	sqlDB := stdlib.OpenDB(*cfg)
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(2)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	s := &Store{DB: sqlDB, engine: PostgreSQL, cleanup: cleanup}
	if err = sqlDB.PingContext(ctx); err != nil {
		_ = s.Close()
		return nil, fmt.Errorf("open PostgreSQL database: %w", err)
	}
	if err = migrate(ctx, sqlDB, PostgreSQL); err != nil {
		_ = s.Close()
		return nil, err
	}
	qdb := postgres.New(sqlDB)
	s.Queries = db.New(postgresQueries{qdb}, func(tx *sql.Tx) db.Querier { return postgresQueries{qdb.WithTx(tx)} })
	return s, nil
}

func migrate(ctx context.Context, sqlDB *sql.DB, engine Engine) error {
	dialect, dir := goose.DialectSQLite3, "migrations/sqlite"
	if engine == PostgreSQL {
		dialect, dir = goose.DialectPostgres, "migrations/postgres"
	}
	provider, err := goose.NewProvider(dialect, sqlDB, mustSub(migrations, dir))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err = provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate %s: %w", engine, err)
	}
	return nil
}

// Engine reports the selected database dialect.
func (s *Store) Engine() Engine { return s.engine }

// Tx runs fn atomically, committing only if fn returns nil.
func (s *Store) Tx(ctx context.Context, fn func(q *db.Queries) error) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err = fn(s.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit()
}

// Close closes the connection pool and drops an isolated test schema, if any.
// It is safe to call repeatedly; cleanup does not inherit a cancelled request.
func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		s.closeErr = s.DB.Close()
		if s.cleanup != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			s.closeErr = errors.Join(s.closeErr, s.cleanup(ctx))
		}
	})
	return s.closeErr
}

// RenameAgentSessionTitle sets a session's title.
func (s *Store) RenameAgentSessionTitle(ctx context.Context, id, title string) error {
	return s.RenameSession(ctx, db.RenameSessionParams{Title: title, UpdatedAt: time.Now().Unix(), ID: id})
}

// Snapshot writes a consistent SQLite copy while the server keeps running.
// path must not exist. PostgreSQL uses the operator's external backup tools.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	if s.engine != SQLite {
		return ErrSnapshotUnsupported
	}
	_, err := s.DB.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}
