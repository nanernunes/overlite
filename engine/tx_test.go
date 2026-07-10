package engine

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTransactionCommitAndRollback(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()
	mustExec(t, eng, `CREATE TABLE t (n INTEGER)`)

	count := func() int64 {
		rs, err := eng.Execute(ctx, `SELECT count(*) FROM t`, nil)
		require.NoError(t, err)
		return rs.Rows[0][0].(int64)
	}

	// Commit persists.
	tx, err := eng.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Execute(ctx, `INSERT INTO t (n) VALUES (1)`, nil)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	assert.Equal(t, int64(1), count())

	// Rollback reverts.
	tx2, err := eng.Begin(ctx)
	require.NoError(t, err)
	_, err = tx2.Execute(ctx, `INSERT INTO t (n) VALUES (2)`, nil)
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback())
	assert.Equal(t, int64(1), count()) // the second insert is gone
}

func TestTransactionIsolatedUntilCommit(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()
	mustExec(t, eng, `CREATE TABLE t (n INTEGER)`)

	tx, err := eng.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Execute(ctx, `INSERT INTO t (n) VALUES (42)`, nil)
	require.NoError(t, err)

	// Within the transaction the row is visible.
	rs, err := tx.Execute(ctx, `SELECT n FROM t`, nil)
	require.NoError(t, err)
	require.Len(t, rs.Rows, 1)

	require.NoError(t, tx.Rollback())
	after, err := eng.Execute(ctx, `SELECT count(*) FROM t`, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(0), after.Rows[0][0])
}
