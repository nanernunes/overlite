package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration tools take an advisory lock so two deploys never migrate at once;
// Prisma's `migrate deploy` failed outright on "no such function:
// pg_advisory_lock". The locks are now held by the server, across connections.
func TestAdvisoryLocks(t *testing.T) {
	addr := startServer(t)
	a, b, c := connectExtended(t, addr), connect(t, addr), connectExtended(t, addr)
	ctx := context.Background()

	tryLock := func(sql string, args ...any) bool {
		t.Helper()
		var ok bool
		require.NoError(t, b.QueryRow(ctx, sql, args...).Scan(&ok))
		return ok
	}

	// A holds 72707369, as Prisma takes it: B can't have it until A lets go.
	_, err := a.Exec(ctx, `SELECT pg_advisory_lock(72707369)`)
	require.NoError(t, err)
	assert.False(t, tryLock(`SELECT pg_try_advisory_lock(72707369)`))
	assert.True(t, tryLock(`SELECT pg_try_advisory_lock(1, 2)`), "two keys are another lock")
	assert.False(t, tryLock(`SELECT pg_advisory_unlock(72707369)`), "B does not hold it")

	// C waits for it, by a bound parameter as golang-migrate asks, until A
	// unlocks.
	got := make(chan error, 1)
	go func() {
		_, err := c.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(72707369))
		got <- err
	}()
	select {
	case <-got:
		t.Fatal("C took a lock A holds")
	case <-time.After(100 * time.Millisecond):
	}
	var unlocked bool
	require.NoError(t, a.QueryRow(ctx, `SELECT pg_advisory_unlock(72707369)`).Scan(&unlocked))
	assert.True(t, unlocked)
	select {
	case err := <-got:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("C never got the lock")
	}

	// The column is named after the function, as in Postgres.
	rows, err := a.Query(ctx, `SELECT pg_try_advisory_lock(72707369)`)
	require.NoError(t, err)
	assert.Equal(t, "pg_try_advisory_lock", rows.FieldDescriptions()[0].Name)
	var held bool
	require.True(t, rows.Next())
	require.NoError(t, rows.Scan(&held))
	assert.False(t, held, "C holds it now")
	rows.Close()

	// A transaction-level lock goes when the transaction does.
	tx, err := a.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7)`)
	require.NoError(t, err)
	assert.False(t, tryLock(`SELECT pg_try_advisory_lock(7)`))
	require.NoError(t, tx.Commit(ctx))
	assert.True(t, tryLock(`SELECT pg_try_advisory_lock(7)`))

	// Shared locks share; an exclusive one waits for them all.
	assert.True(t, tryLock(`SELECT pg_try_advisory_lock_shared(9)`))
	var shared bool
	require.NoError(t, a.QueryRow(ctx, `SELECT pg_try_advisory_lock_shared(9)`).Scan(&shared))
	assert.True(t, shared)
	require.NoError(t, a.QueryRow(ctx, `SELECT pg_try_advisory_lock(9)`).Scan(&shared))
	assert.False(t, shared, "B shares it")

	// Closing the connection lets go of everything it held.
	require.NoError(t, c.Close(ctx))
	var free bool
	require.Eventually(t, func() bool {
		return a.QueryRow(ctx, `SELECT pg_try_advisory_lock(72707369)`).Scan(&free) == nil && free
	}, 2*time.Second, 20*time.Millisecond)
}
