package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestArrayRoundTrip: array columns (stored as JSON) round-trip through the wire
// as real Postgres arrays — built with ARRAY[...] and with '{...}'::type[], read
// back into Go slices.
func TestArrayRoundTrip(t *testing.T) {
	conn := connect(t, startServer(t)) // simple protocol (text), like psql
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, tags text[], nums int[])`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, ARRAY['a','b'], ARRAY[10,20])`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (2, '{c,d}'::text[], '{30,40}'::int[])`)
	require.NoError(t, err)

	var tags []string
	var nums []int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT tags, nums FROM t WHERE id = 1`).Scan(&tags, &nums))
	assert.Equal(t, []string{"a", "b"}, tags)
	assert.Equal(t, []int64{10, 20}, nums)

	require.NoError(t, conn.QueryRow(ctx, `SELECT tags, nums FROM t WHERE id = 2`).Scan(&tags, &nums))
	assert.Equal(t, []string{"c", "d"}, tags)
	assert.Equal(t, []int64{30, 40}, nums)

	// An empty array and one with a quoted element (contains a comma).
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (3, ARRAY['x,y'], ARRAY[]::int[])`)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `SELECT tags, nums FROM t WHERE id = 3`).Scan(&tags, &nums))
	assert.Equal(t, []string{"x,y"}, tags)
	assert.Equal(t, []int64{}, nums)
}
