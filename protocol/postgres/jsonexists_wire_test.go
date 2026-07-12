package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJSONBExists: the jsonb key-existence operators ? / ?| / ?& work (the ?
// no longer collides with a SQLite bind parameter).
func TestJSONBExists(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, data jsonb)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, '{"a":1,"b":2}'), (2, '{"c":3}')`)
	require.NoError(t, err)

	// ? : key exists.
	var id, cnt int
	require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM t WHERE data ? 'c'`).Scan(&id))
	assert.Equal(t, 2, id)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE data ? 'a'`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// ?| : any of the keys.
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE data ?| array['a','z']`).Scan(&cnt))
	assert.Equal(t, 1, cnt)

	// ?& : all of the keys.
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE data ?& array['a','b']`).Scan(&cnt))
	assert.Equal(t, 1, cnt)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE data ?& array['a','c']`).Scan(&cnt))
	assert.Equal(t, 0, cnt)

	// In the target list too (1/0, as SQLite has no bool).
	var exists int
	require.NoError(t, conn.QueryRow(ctx, `SELECT data ? 'b' FROM t WHERE id = 1`).Scan(&exists))
	assert.Equal(t, 1, exists)
}
