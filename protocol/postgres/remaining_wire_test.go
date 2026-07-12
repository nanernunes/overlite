package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLateral: LATERAL over a set-returning function works (the keyword is
// stripped; SQLite treats table-valued functions as implicitly lateral).
func TestLateral(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE docs (id int, data jsonb)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO docs VALUES (1,'[10,20]'), (2,'[30]')`)
	require.NoError(t, err)

	rows, err := conn.Query(ctx,
		`SELECT d.id, x.value FROM docs d, LATERAL json_each(d.data) x ORDER BY d.id, x.value`)
	require.NoError(t, err)
	defer rows.Close()
	var got [][2]int
	for rows.Next() {
		var id, v int
		require.NoError(t, rows.Scan(&id, &v))
		got = append(got, [2]int{id, v})
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, [][2]int{{1, 10}, {1, 20}, {2, 30}}, got)
}

// TestDefaultNextval: DEFAULT nextval('seq') in a column definition assigns
// sequence values on insert when the column is omitted.
func TestDefaultNextval(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE SEQUENCE s START 100`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `CREATE TABLE t (id int DEFAULT nextval('s'), name text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t (name) VALUES ('a')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t (name) VALUES ('b')`)
	require.NoError(t, err)

	rows, err := conn.Query(ctx, `SELECT id FROM t ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var ids []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []int{100, 101}, ids)
}

// TestCreateFunction: CREATE/DROP FUNCTION are accepted (bodies not executed).
func TestCreateFunction(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx,
		`CREATE FUNCTION addy(a int, b int) RETURNS int LANGUAGE sql AS $$ SELECT a + b $$`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `CREATE OR REPLACE FUNCTION noop() RETURNS void AS $$ BEGIN END $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `DROP FUNCTION addy(int, int)`)
	require.NoError(t, err)
}

// TestRange: range constructors, accessors, and containment.
func TestRange(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var r string
	require.NoError(t, conn.QueryRow(ctx, `SELECT int4range(1, 10)`).Scan(&r))
	assert.Equal(t, "[1,10)", r)

	var lo, hi string
	require.NoError(t, conn.QueryRow(ctx, `SELECT lower(int4range(1,10)), upper(int4range(1,10))`).Scan(&lo, &hi))
	assert.Equal(t, "1", lo)
	assert.Equal(t, "10", hi)

	// @> containment (returns 0/1; SQLite has no bool).
	var in, out int
	require.NoError(t, conn.QueryRow(ctx, `SELECT int4range(1,10) @> 5`).Scan(&in))
	require.NoError(t, conn.QueryRow(ctx, `SELECT int4range(1,10) @> 10`).Scan(&out))
	assert.Equal(t, 1, in)
	assert.Equal(t, 0, out) // upper bound exclusive

	var empty int
	require.NoError(t, conn.QueryRow(ctx, `SELECT isempty(int4range(5,5))`).Scan(&empty))
	assert.Equal(t, 1, empty)

	// A range column round-trips.
	_, err := conn.Exec(ctx, `CREATE TABLE t (r int4range)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES ('[3,7)')`)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `SELECT r FROM t`).Scan(&r))
	assert.Equal(t, "[3,7)", r)
}
