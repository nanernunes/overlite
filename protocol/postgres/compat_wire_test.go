package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres-isms that used to fail to parse. Each is a syntax error away from
// working, which is a hard stop for a client that emits it.

func TestILike(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, name text)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 'Printer Jam'), (2, 'vpn down')`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name ILIKE '%printer%'`).Scan(&n))
	assert.Equal(t, 1, n, "ILIKE should match regardless of case")

	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name ILIKE '%VPN%'`).Scan(&n))
	assert.Equal(t, 1, n)

	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name NOT ILIKE '%printer%'`).Scan(&n))
	assert.Equal(t, 1, n)

	// A literal containing the word is not an operator.
	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'ilike'`).Scan(&s))
	assert.Equal(t, "ilike", s)
}

func TestSelectForUpdate(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, n int)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 10)`)

	for _, sql := range []string{
		`SELECT n FROM t WHERE id = 1 FOR UPDATE`,
		`SELECT n FROM t WHERE id = 1 FOR NO KEY UPDATE`,
		`SELECT n FROM t WHERE id = 1 FOR SHARE`,
		`SELECT n FROM t WHERE id = 1 FOR KEY SHARE`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE NOWAIT`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE SKIP LOCKED`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE OF t`,
	} {
		var n int
		require.NoErrorf(t, conn.QueryRow(ctx, sql).Scan(&n), "%s", sql)
		assert.Equal(t, 10, n)
	}

	// A counter read under FOR UPDATE, the usual reason to write one.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	var n int
	require.NoError(t, tx.QueryRow(ctx, `SELECT n FROM t WHERE id = 1 FOR UPDATE`).Scan(&n))
	_, err = tx.Exec(ctx, `UPDATE t SET n = $1 WHERE id = 1`, n+1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	require.NoError(t, conn.QueryRow(ctx, `SELECT n FROM t WHERE id = 1`).Scan(&n))
	assert.Equal(t, 11, n)
}

func TestComputedColumnDefaults(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (
		id uuid DEFAULT gen_random_uuid(),
		created_at timestamptz DEFAULT now(),
		label text DEFAULT 'none',
		n int DEFAULT 0)`)
	mustExec(t, conn, `INSERT INTO t (label) VALUES ('x')`)

	var id, createdAt, label string
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT id, created_at, label, n FROM t`).Scan(&id, &createdAt, &label, &n))
	assert.Len(t, id, 36, "the uuid default did not run")
	assert.NotEmpty(t, createdAt, "the now() default did not run")
	assert.Equal(t, "x", label)
	assert.Equal(t, 0, n)

	// The same through ALTER TABLE.
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN touched_at timestamptz DEFAULT now()`)
	mustExec(t, conn, `INSERT INTO t (label) VALUES ('y')`)
	var touched string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT touched_at FROM t WHERE label = 'y'`).Scan(&touched))
	assert.NotEmpty(t, touched)
}

// A computed default through ADD COLUMN: SQLite refuses it outright, so the
// table is rebuilt with the column in its CREATE TABLE. Existing rows keep
// their data and take the new column's default.
func TestAddColumnComputedDefaultKeepsData(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, label text)`)
	mustExec(t, conn, `CREATE INDEX idx_label ON t (label)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 'first'), (2, 'second')`)

	mustExec(t, conn, `ALTER TABLE t ADD COLUMN created_at timestamptz DEFAULT now()`)

	// The existing rows are still there, with their data.
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)

	var label string
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM t WHERE id = 2`).Scan(&label))
	assert.Equal(t, "second", label)

	// New rows get the default.
	mustExec(t, conn, `INSERT INTO t (id, label) VALUES (3, 'third')`)
	var createdAt string
	require.NoError(t, conn.QueryRow(ctx, `SELECT created_at FROM t WHERE id = 3`).Scan(&createdAt))
	assert.NotEmpty(t, createdAt)

	// The index survived the rebuild.
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename = 't' AND indexname = 'idx_label'`).Scan(&n))
	assert.Equal(t, 1, n, "the index was lost in the rebuild")

	// A constant default still takes the plain path.
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN n int DEFAULT 0`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT n FROM t WHERE id = 1`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestAddColumnComputedDefaultExtendedProtocol(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1)`)
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN created_at timestamptz DEFAULT now()`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
}
