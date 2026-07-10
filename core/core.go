// Package core defines the neutral data model that sits between a wire
// protocol (Postgres, MySQL, HTTP, ...) and the storage engine (SQLite).
//
// A protocol never touches SQLite directly: it speaks its own dialect on the
// wire, hands a plain SQL string + args to a core.Engine, and renders the
// resulting core.ResultSet back in its own encoding. This is what lets us grow
// from one protocol to many without touching storage.
package core

import "context"

// Value is a single cell. It carries whatever Go type the engine produced
// (int64, float64, string, []byte, bool, time.Time, nil). Protocols decide how
// to encode it on their wire.
type Value = any

// Column describes one output column.
type Column struct {
	Name string
	// DeclType is the declared type as SQLite reports it (e.g. "INTEGER",
	// "TEXT", "BLOB"). It is a hint for protocols that must advertise a static
	// type before any row is seen; it may be empty.
	DeclType string
}

// ResultSet is the neutral outcome of executing one statement.
type ResultSet struct {
	// IsQuery is true when the statement returned rows (SELECT, PRAGMA,
	// RETURNING, ...). When false, only the counters below are meaningful.
	IsQuery bool

	Columns []Column
	Rows    [][]Value

	RowsAffected int64
	LastInsertID int64

	// Command is the leading verb of the statement, upper-cased
	// (e.g. "SELECT", "INSERT", "CREATE TABLE"). Protocols use it to build
	// their command-complete tag.
	Command string
}

// Engine is the storage abstraction. Today the only implementation is SQLite,
// but the protocols depend on this interface, never on a concrete engine.
type Engine interface {
	// Execute runs a single SQL statement and returns its result. args are
	// bound positionally; pass nil when there are none.
	Execute(ctx context.Context, sql string, args []Value) (*ResultSet, error)

	// Describe returns the output columns of a query without returning its
	// rows, for protocols (e.g. Postgres extended query) that must advertise a
	// row description before execution. It returns nil columns for statements
	// that produce no rows. args supplies placeholder values (may be nil); it
	// only affects introspection, never mutates data.
	Describe(ctx context.Context, sql string, args []Value) ([]Column, error)

	// Begin starts a transaction. Statements run through the returned Tx are
	// atomic; the engine may serialize concurrent transactions.
	Begin(ctx context.Context) (Tx, error)

	Close() error
}

// Tx is an in-progress transaction. Execute/Describe behave like the engine's
// but run within the transaction.
type Tx interface {
	Execute(ctx context.Context, sql string, args []Value) (*ResultSet, error)
	Describe(ctx context.Context, sql string, args []Value) ([]Column, error)
	Commit() error
	Rollback() error
}

// SchemaManager is implemented by engines that support Postgres-style schemas.
// A protocol type-asserts to it to handle CREATE/DROP SCHEMA. Optional: engines
// without multi-schema support simply don't implement it.
type SchemaManager interface {
	CreateSchema(ctx context.Context, name string, ifNotExists bool) error
	DropSchema(ctx context.Context, name string, ifExists, cascade bool) error
}
