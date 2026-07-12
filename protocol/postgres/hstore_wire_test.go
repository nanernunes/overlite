package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHstore: an hstore column stores a key/value map (as JSON), round-trips as
// hstore text, and supports -> (value) and ? (key existence).
func TestHstore(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, h hstore)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, 'a=>1, b=>2'::hstore), (2, 'x=>10'::hstore)`)
	require.NoError(t, err)

	// Output in hstore text form (keys sorted for stability).
	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT h FROM t WHERE id = 1`).Scan(&s))
	assert.Equal(t, `"a"=>"1", "b"=>"2"`, s)

	// -> value access (value text via ->>).
	var v string
	require.NoError(t, conn.QueryRow(ctx, `SELECT h ->> 'a' FROM t WHERE id = 1`).Scan(&v))
	assert.Equal(t, "1", v)

	// ? key existence.
	var yes, no int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE h ? 'b'`).Scan(&yes))
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE h ? 'z'`).Scan(&no))
	assert.Equal(t, 1, yes)
	assert.Equal(t, 0, no)

	// akeys / avals (sorted).
	var keys, vals string
	require.NoError(t, conn.QueryRow(ctx, `SELECT akeys(h) FROM t WHERE id = 1`).Scan(&keys))
	require.NoError(t, conn.QueryRow(ctx, `SELECT avals(h) FROM t WHERE id = 1`).Scan(&vals))
	assert.Equal(t, `["a","b"]`, keys)
	assert.Equal(t, `["1","2"]`, vals)
}
