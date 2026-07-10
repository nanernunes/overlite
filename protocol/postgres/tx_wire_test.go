package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTxCommitOverWire(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (n INTEGER)`)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t (n) VALUES (1)`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestTxRollbackOverWire(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (n INTEGER)`)
	mustExec(t, conn, `INSERT INTO t (n) VALUES (1)`)

	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `INSERT INTO t (n) VALUES (2)`)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(ctx))

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n) // the rolled-back insert is gone
}

// A failed statement inside a transaction aborts it: further statements are
// rejected until ROLLBACK, and the whole transaction's effects are discarded.
func TestTxAbortedState(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (n INTEGER)`)

	_, err := conn.Exec(ctx, `BEGIN`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t (n) VALUES (1)`)
	require.NoError(t, err)

	// Trigger an error → transaction becomes aborted.
	_, err = conn.Exec(ctx, `SELECT * FROM does_not_exist`)
	require.Error(t, err)

	// Subsequent statements are rejected until the block ends.
	_, err = conn.Exec(ctx, `INSERT INTO t (n) VALUES (2)`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aborted")

	_, err = conn.Exec(ctx, `ROLLBACK`)
	require.NoError(t, err)

	// Nothing from the aborted transaction persisted.
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 0, n)
}
