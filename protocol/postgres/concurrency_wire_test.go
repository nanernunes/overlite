package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With one dedicated connection per client, a client holding an open
// transaction must not block another client's reads (this deadlocked under the
// old single-connection engine).
func TestConcurrentReadDuringWriteTransaction(t *testing.T) {
	addr := startServer(t)
	a := connect(t, addr)
	b := connect(t, addr)
	ctx := context.Background()

	mustExec(t, a, `CREATE TABLE t (n INTEGER)`)
	mustExec(t, a, `INSERT INTO t VALUES (1)`) // committed

	// A opens a transaction and writes, but does not commit yet.
	txA, err := a.Begin(ctx)
	require.NoError(t, err)
	_, err = txA.Exec(ctx, `INSERT INTO t VALUES (2)`)
	require.NoError(t, err)

	// B must read concurrently within a short timeout — no blocking on A's tx.
	ctxB, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var n int
	require.NoError(t, b.QueryRow(ctxB, `SELECT count(*) FROM t`).Scan(&n),
		"read blocked by another client's open transaction")
	assert.Equal(t, 1, n, "B sees only committed rows, not A's uncommitted insert")

	require.NoError(t, txA.Commit(ctx))
	require.NoError(t, b.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)
}

// Many clients writing and reading at once all make progress.
func TestManyConcurrentClients(t *testing.T) {
	addr := startServer(t)
	setup := connect(t, addr)
	ctx := context.Background()
	mustExec(t, setup, `CREATE TABLE t (id INTEGER PRIMARY KEY, who INTEGER)`)

	const clients = 20
	dsn := fmt.Sprintf("postgres://postgres@%s/test?sslmode=disable", addr)

	var wg sync.WaitGroup
	errs := make([]error, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = func() error {
				c, err := pgx.Connect(ctx, dsn)
				if err != nil {
					return err
				}
				defer c.Close(ctx)
				for j := 0; j < 5; j++ {
					if _, err := c.Exec(ctx, `INSERT INTO t (who) VALUES ($1)`, i); err != nil {
						return err
					}
				}
				var n int
				if err := c.QueryRow(ctx, `SELECT count(*) FROM t WHERE who = $1`, i).Scan(&n); err != nil {
					return err
				}
				if n != 5 {
					return fmt.Errorf("client %d saw %d of its rows, want 5", i, n)
				}
				return nil
			}()
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		require.NoErrorf(t, err, "client %d", i)
	}
	var total int
	require.NoError(t, setup.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&total))
	assert.Equal(t, clients*5, total)
}
