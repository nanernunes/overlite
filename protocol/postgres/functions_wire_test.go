package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSQLFunctions: LANGUAGE sql functions are executed (by inlining) — scalar
// and set-returning, named and positional params, nested, replaceable, drop.
func TestSQLFunctions(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	exec := func(sql string) {
		_, err := conn.Exec(ctx, sql)
		require.NoError(t, err, sql)
	}
	scalar := func(sql string) int {
		var n int
		require.NoError(t, conn.QueryRow(ctx, sql).Scan(&n))
		return n
	}

	// Named parameters.
	exec(`CREATE FUNCTION add2(a int, b int) RETURNS int AS $$ SELECT a + b $$ LANGUAGE sql`)
	assert.Equal(t, 7, scalar(`SELECT add2(3, 4)`))

	// Positional parameters ($1, $2).
	exec(`CREATE FUNCTION mul(x int, y int) RETURNS int AS $$ SELECT $1 * $2 $$ LANGUAGE sql`)
	assert.Equal(t, 42, scalar(`SELECT mul(6, 7)`))

	// A function reading a table, used in an expression and a predicate.
	exec(`CREATE TABLE emp (id int, sal int)`)
	exec(`INSERT INTO emp VALUES (1, 100), (2, 300), (3, 200)`)
	exec(`CREATE FUNCTION top_sal() RETURNS int AS $$ SELECT max(sal) FROM emp $$ LANGUAGE sql`)
	assert.Equal(t, 300, scalar(`SELECT top_sal()`))
	assert.Equal(t, 2, scalar(`SELECT id FROM emp WHERE sal = top_sal()`))

	// Nested calls fully inline.
	exec(`CREATE FUNCTION inc(x int) RETURNS int AS $$ SELECT x + 1 $$ LANGUAGE sql`)
	exec(`CREATE FUNCTION inc2(x int) RETURNS int AS $$ SELECT inc(inc(x)) $$ LANGUAGE sql`)
	assert.Equal(t, 12, scalar(`SELECT inc2(10)`))

	// CREATE OR REPLACE swaps the body.
	exec(`CREATE OR REPLACE FUNCTION add2(a int, b int) RETURNS int AS $$ SELECT a * b $$ LANGUAGE sql`)
	assert.Equal(t, 12, scalar(`SELECT add2(3, 4)`))

	// Set-returning function used in FROM.
	exec(`CREATE FUNCTION emps_over(m int) RETURNS TABLE(id int, sal int)
	      AS $$ SELECT id, sal FROM emp WHERE sal > m ORDER BY id $$ LANGUAGE sql`)
	rows, err := conn.Query(ctx, `SELECT id, sal FROM emps_over(150) x ORDER BY id`)
	require.NoError(t, err)
	defer rows.Close()
	var got [][2]int
	for rows.Next() {
		var id, sal int
		require.NoError(t, rows.Scan(&id, &sal))
		got = append(got, [2]int{id, sal})
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, [][2]int{{2, 300}, {3, 200}}, got)

	// DROP removes it.
	_, err = conn.Exec(ctx, `DROP FUNCTION mul(int, int)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `SELECT mul(1, 2)`)
	require.Error(t, err)

	// A non-sql language is still accepted (no-op), not executed.
	_, err = conn.Exec(ctx, `CREATE FUNCTION pl() RETURNS int AS $$ BEGIN RETURN 1; END $$ LANGUAGE plpgsql`)
	require.NoError(t, err)
}
