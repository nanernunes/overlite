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

// TestArrayOps: element access, length, and membership on stored arrays.
func TestArrayOps(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, tags text[], nums int[])`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, ARRAY['a','b','c'], ARRAY[10,20,30])`)
	require.NoError(t, err)

	// Subscript is 1-based.
	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT tags[2] FROM t`).Scan(&s))
	assert.Equal(t, "b", s)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT nums[1] FROM t`).Scan(&n))
	assert.Equal(t, 10, n)

	// array_length / cardinality.
	var l, c int
	require.NoError(t, conn.QueryRow(ctx, `SELECT array_length(tags, 1) FROM t`).Scan(&l))
	assert.Equal(t, 3, l)
	require.NoError(t, conn.QueryRow(ctx, `SELECT cardinality(nums) FROM t`).Scan(&c))
	assert.Equal(t, 3, c)

	// = ANY(arr) membership (0/1, as SQLite has no bool), and in WHERE.
	var found, missing int
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'b' = ANY(tags) FROM t`).Scan(&found))
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'z' = ANY(tags) FROM t`).Scan(&missing))
	assert.Equal(t, 1, found)
	assert.Equal(t, 0, missing)
	var cnt int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE 20 = ANY(nums)`).Scan(&cnt))
	assert.Equal(t, 1, cnt)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE 99 = ANY(nums)`).Scan(&cnt))
	assert.Equal(t, 0, cnt)
}

// TestArrayUnnest: unnest expands an array to a set of rows.
func TestArrayUnnest(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, tags text[])`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, ARRAY['a','b','c'])`)
	require.NoError(t, err)

	rows, err := conn.Query(ctx, `SELECT unnest(tags) FROM t ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		require.NoError(t, rows.Scan(&v))
		got = append(got, v)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"a", "b", "c"}, got)
}
