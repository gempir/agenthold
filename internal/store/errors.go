package store

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// IsUniqueViolation reports unique/primary-key conflicts without depending on
// database-specific error text. Foreign-key and other constraint failures are
// distinct errors and must not be presented as duplicate names.
func IsUniqueViolation(err error) bool {
	var pgError *pgconn.PgError
	if errors.As(err, &pgError) {
		return pgError.Code == "23505"
	}
	var sqliteError *sqlite.Error
	if errors.As(err, &sqliteError) {
		return sqliteError.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || sqliteError.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
	}
	return false
}
