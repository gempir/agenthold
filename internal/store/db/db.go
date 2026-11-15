// Package db is the engine-neutral query contract consumed by the application.
// sqlc generates each engine independently; types.go preserves the canonical
// row and parameter types, and store adapts PostgreSQL rows at compile time.
package db

import "database/sql"

// Queries binds the canonical interface to one database or transaction.
type Queries struct {
	Querier
	bind func(*sql.Tx) Querier
}

// New wraps an implementation with its engine-specific transaction binder.
func New(q Querier, bind func(*sql.Tx) Querier) *Queries { return &Queries{Querier: q, bind: bind} }

// WithTx returns the same query contract bound to tx.
func (q *Queries) WithTx(tx *sql.Tx) *Queries { return New(q.bind(tx), q.bind) }
