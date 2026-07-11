package postgres_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoOpUtilityStatements checks statements we accept but don't model run
// without error (so migrations/dumps proceed).
func TestNoOpUtilityStatements(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (id int)`)
	for _, sql := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,
		`DROP EXTENSION IF EXISTS pgcrypto`,
		`COMMENT ON TABLE t IS 'a table'`,
		`COMMENT ON COLUMN t.id IS 'the id'`,
		`NOTIFY some_channel`,
	} {
		mustExec(t, conn, sql)
	}
	// The connection still works afterwards.
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestAlterTable(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (id int, name text)`)
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN email text`)
	mustExec(t, conn, `ALTER TABLE t RENAME COLUMN name TO full_name`)
	mustExec(t, conn, `ALTER TABLE t DROP COLUMN email`)
	mustExec(t, conn, `ALTER TABLE t RENAME TO people`)

	assert.Equal(t, []string{"id", "full_name"}, queryColumn(t, conn,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'people' ORDER BY ordinal_position`, 0))
}

func TestIsolationLevelStatements(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	// Accepted (no real effect): SQLite serializes writes anyway.
	mustExec(t, conn, `SET default_transaction_isolation = 'serializable'`)
	tx, err := conn.Begin(ctx) // pgx BEGIN
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL SERIALIZABLE`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))
}

func TestBooleanType(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE flags (id int, active boolean)`)
	mustExec(t, conn, `INSERT INTO flags VALUES (1, true), (2, false)`)

	// A declared boolean column scans as Go bool (advertised OID is bool, so the
	// stored 0/1 is sent as t/f).
	var active bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT active FROM flags WHERE id = 1`).Scan(&active))
	assert.True(t, active)
	require.NoError(t, conn.QueryRow(ctx, `SELECT active FROM flags WHERE id = 2`).Scan(&active))
	assert.False(t, active)

	// Round-trip a bool parameter.
	mustExec(t, conn, `CREATE TABLE p (b boolean)`)
	_, err := conn.Exec(ctx, `INSERT INTO p (b) VALUES ($1)`, true)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx, `SELECT b FROM p`).Scan(&active))
	assert.True(t, active)
}

func TestJSONContainment(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE docs (id int, body jsonb)`)
	mustExec(t, conn, `INSERT INTO docs VALUES
		(1, '{"name":"ada","tags":["a","b"],"meta":{"x":1}}'),
		(2, '{"name":"bob","tags":["c"]}')`)

	// @> object containment.
	assert.Equal(t, []string{"1"}, queryColumn(t, conn,
		`SELECT id::text FROM docs WHERE body @> '{"name":"ada"}' ORDER BY id`, 0))
	// @> nested + array element containment.
	assert.Equal(t, []string{"1"}, queryColumn(t, conn,
		`SELECT id::text FROM docs WHERE body @> '{"meta":{"x":1}}'`, 0))
	assert.Equal(t, []string{"1"}, queryColumn(t, conn,
		`SELECT id::text FROM docs WHERE body @> '{"tags":["b"]}'`, 0))
	// No match.
	assert.Empty(t, queryColumn(t, conn,
		`SELECT id::text FROM docs WHERE body @> '{"name":"zed"}'`, 0))
	// <@ (contained by): the literal on the left is contained in the column.
	assert.Equal(t, []string{"2"}, queryColumn(t, conn,
		`SELECT id::text FROM docs WHERE '{"name":"bob"}' <@ body`, 0))
}

func TestIntervalArithmetic(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	cases := []struct{ query, want string }{
		{`SELECT '2020-01-15 10:00:00' + interval '2 hours'`, "2020-01-15 12:00:00"},
		{`SELECT '2020-01-15' - interval '1 day'`, "2020-01-14 00:00:00"},
		{`SELECT '2020-01-15' + interval '1 year 2 months'`, "2021-03-15 00:00:00"},
		{`SELECT '2020-01-01' + interval '2 weeks'`, "2020-01-15 00:00:00"},
		{`SELECT '2020-01-15 10:00:00' - interval '30 minutes'`, "2020-01-15 09:30:00"},
	}
	for _, c := range cases {
		var got string
		require.NoErrorf(t, conn.QueryRow(ctx, c.query).Scan(&got), "query %q", c.query)
		assert.Equalf(t, c.want, got, "query %q", c.query)
	}
}

func TestSavepoints(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id int)`)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t VALUES (1)`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SAVEPOINT sp1`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t VALUES (2)`)
	require.NoError(t, err)
	// Roll back to the savepoint: row 2 is undone, row 1 stays.
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp1`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t VALUES (3)`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `RELEASE SAVEPOINT sp1`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	assert.Equal(t, []string{"1", "3"}, queryColumn(t, conn, `SELECT id::text FROM t ORDER BY id`, 0))
}

// TestSavepointRecoversAbortedTx checks ROLLBACK TO clears the aborted state so
// the transaction can continue (Postgres error-recovery semantics).
func TestSavepointRecoversAbortedTx(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id int PRIMARY KEY)`)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SAVEPOINT sp`)
	require.NoError(t, err)
	// Cause an error, aborting the (sub)transaction.
	_, err = tx.Exec(ctx, `INSERT INTO t VALUES ('not a number' + 1)`)
	_ = err
	_, err = tx.Exec(ctx, `SELECT bogus_col`)
	require.Error(t, err)
	// Recover by rolling back to the savepoint, then continue.
	_, err = tx.Exec(ctx, `ROLLBACK TO SAVEPOINT sp`)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t VALUES (42)`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	assert.Equal(t, []string{"42"}, queryColumn(t, conn, `SELECT id::text FROM t`, 0))
}

// TestOuterJoins covers RIGHT/FULL OUTER JOIN, which recent SQLite runs
// natively (pass-through).
func TestOuterJoins(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE a (id int)`)
	mustExec(t, conn, `CREATE TABLE b (id int)`)
	mustExec(t, conn, `INSERT INTO a VALUES (1),(2),(3)`)
	mustExec(t, conn, `INSERT INTO b VALUES (2),(3),(4)`)

	assert.Equal(t, []string{"2", "3", "4"}, queryColumn(t, conn,
		`SELECT b.id::text FROM a RIGHT JOIN b ON a.id = b.id ORDER BY b.id`, 0))
	assert.Equal(t, []string{"1", "2", "3", "4"}, queryColumn(t, conn,
		`SELECT COALESCE(a.id, b.id)::text FROM a FULL OUTER JOIN b ON a.id = b.id ORDER BY 1`, 0))
}

func TestGenerateSeries(t *testing.T) {
	conn := connect(t, startServer(t))

	assert.Equal(t, []string{"1", "2", "3", "4", "5"}, queryColumn(t, conn,
		`SELECT generate_series::text FROM generate_series(1, 5)`, 0))
	// Step, including descending, and empty ranges.
	assert.Equal(t, []string{"0", "2", "4", "6"}, queryColumn(t, conn,
		`SELECT generate_series::text FROM generate_series(0, 6, 2)`, 0))
	assert.Equal(t, []string{"5", "3", "1"}, queryColumn(t, conn,
		`SELECT generate_series::text FROM generate_series(5, 1, -2)`, 0))
	assert.Empty(t, queryColumn(t, conn,
		`SELECT generate_series::text FROM generate_series(5, 1)`, 0))
	// Usable with an alias and joined like a table.
	assert.Equal(t, []string{"10", "20", "30"}, queryColumn(t, conn,
		`SELECT (g * 10)::text FROM generate_series(1, 3) AS g`, 0))
}

func TestDistinctOn(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE sales (region text, amount int, ts int)`)
	mustExec(t, conn, `INSERT INTO sales VALUES
		('west', 10, 1), ('west', 30, 2), ('east', 20, 1), ('east', 5, 3)`)

	// Latest row per region (highest ts): DISTINCT ON (region) ... ORDER BY ts DESC.
	rows, err := conn.Query(context.Background(),
		`SELECT DISTINCT ON (region) region, amount FROM sales ORDER BY region, ts DESC`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var r string
		var a int64
		require.NoError(t, rows.Scan(&r, &a))
		got[r] = a
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]int64{"west": 30, "east": 5}, got)
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDTypeAndGenerators(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// gen_random_uuid()/uuid_generate_v4() produce distinct valid v4 UUIDs.
	var u1, u2 string
	require.NoError(t, conn.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&u1))
	require.NoError(t, conn.QueryRow(ctx, `SELECT uuid_generate_v4()`).Scan(&u2))
	assert.Regexp(t, uuidRE, u1)
	assert.Regexp(t, uuidRE, u2)
	assert.NotEqual(t, u1, u2)

	// A uuid column stores the value as text and reports type uuid in the catalog.
	mustExec(t, conn, `CREATE TABLE items (id uuid, name text)`)
	mustExec(t, conn, `INSERT INTO items (id, name) VALUES (gen_random_uuid(), 'a')`)
	var got string
	require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM items`).Scan(&got))
	assert.Regexp(t, uuidRE, got)

	assert.Equal(t, []string{"uuid"}, queryColumn(t, conn,
		`SELECT format_type(atttypid, NULL) FROM pg_catalog.pg_attribute a
		 JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'items' AND a.attname = 'id'`, 0))

	// A non-deterministic generator yields a fresh value per row.
	rows := queryColumn(t, conn,
		`SELECT gen_random_uuid() FROM (SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3)`, 0)
	assert.Len(t, rows, 3)
	assert.NotEqual(t, rows[0], rows[1])
	assert.NotEqual(t, rows[1], rows[2])
}
