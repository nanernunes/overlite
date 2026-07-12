package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollate: Postgres COLLATE clauses are accepted (never a "no such
// collation" error) and mapped to SQLite — case-insensitive collations become
// NOCASE, everything else (C/POSIX/locale) falls back to the byte-order default.
func TestCollate(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// A locale collation is accepted; comparison falls back to byte order.
	_, err := conn.Exec(ctx, `CREATE TABLE loc (tag text COLLATE "en_US")`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO loc VALUES ('Hello')`)
	require.NoError(t, err)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM loc WHERE tag = 'hello'`).Scan(&n))
	assert.Equal(t, 0, n) // byte order: 'Hello' != 'hello'

	// A case-insensitive collation maps to NOCASE.
	_, err = conn.Exec(ctx, `CREATE TABLE ci (name text COLLATE case_insensitive)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO ci VALUES ('Hello')`)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM ci WHERE name = 'hello'`).Scan(&n))
	assert.Equal(t, 1, n) // NOCASE: 'Hello' == 'hello'

	// COLLATE in an expression, both directions (SQLite has no bool: 1/0).
	var ciEq, cEq int
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'ABC' COLLATE case_insensitive = 'abc'`).Scan(&ciEq))
	assert.Equal(t, 1, ciEq)
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'ABC' COLLATE "C" = 'abc'`).Scan(&cEq))
	assert.Equal(t, 0, cEq)
}
