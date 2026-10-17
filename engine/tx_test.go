package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"overlite/core"
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

// Transactions that read and then write, several at once, as an ORM's do: each
// waits its turn, as on Postgres. Begun deferred, two would both read, then
// race to write; the one that lost failed at once — "database is locked"
// (SQLITE_BUSY_SNAPSHOT), which no busy timeout waits out.
func TestConcurrentWritingTransactionsWaitTheirTurn(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()
	mustExec(t, eng, `CREATE TABLE t (n INTEGER)`)

	const writers = 8
	errs := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- func() error {
				s, err := eng.Session(ctx)
				if err != nil {
					return err
				}
				defer s.Close()
				tx, err := s.Begin(ctx)
				if err != nil {
					return err
				}
				defer func() { _ = tx.Rollback() }()
				if _, err := tx.Execute(ctx, `SELECT count(*) FROM t`, nil); err != nil {
					return err
				}
				time.Sleep(10 * time.Millisecond)
				if _, err := tx.Execute(ctx, `INSERT INTO t (n) VALUES ($1)`, []core.Value{int64(i)}); err != nil {
					return err
				}
				return tx.Commit()
			}()
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	rs, err := eng.Execute(ctx, `SELECT count(*) FROM t`, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(writers), rs.Rows[0][0])
}
