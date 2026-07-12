package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateIndexUsing: a CREATE INDEX with a "USING btree" clause (as pg_dump
// emits) runs, and pg_get_indexdef schema-qualifies the table for a schema
// index so the dump restores.
func TestCreateIndexUsing(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// USING btree accepted on a public table.
	_, err := conn.Exec(ctx, "CREATE TABLE pub (id int, v text)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE INDEX pub_v ON pub USING btree (v)")
	require.NoError(t, err)
	var n int
	require.NoError(t, conn.QueryRow(ctx, "SELECT count(*) FROM pg_class WHERE relname='pub_v'").Scan(&n))
	assert.Equal(t, 1, n)

	// A schema index's def qualifies the table (pg_dump restorability).
	_, err = conn.Exec(ctx, "CREATE SCHEMA s")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE s.t (id int)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE INDEX s_t_id ON s.t USING btree (id)")
	require.NoError(t, err)

	var def string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT pg_get_indexdef(ci.oid)
		FROM pg_class ci JOIN pg_class ct ON true
		JOIN pg_index i ON i.indexrelid = ci.oid
		WHERE ci.relname = 's_t_id'`).Scan(&def))
	// The def qualifies the table (so a dump restores into the right schema) and
	// keeps USING btree (psql's \d reads it; the CREATE INDEX rewrite strips it
	// only when such a statement is actually executed, as tested above).
	assert.Contains(t, def, "ON s.t", "schema index def should qualify the table")
	assert.Contains(t, def, "USING btree")
}
