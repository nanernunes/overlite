package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTimestamptz: a timestamptz column stores an absolute instant (normalized to
// UTC), so offsets on input are honored, ordering/comparison is chronological,
// and it round-trips as a real timestamptz.
func TestTimestamptz(t *testing.T) {
	conn := connect(t, startServer(t)) // simple protocol (text), like psql
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, ts timestamptz)`)
	require.NoError(t, err)
	// 10:30+02 == 08:30 UTC; 09:00+00 == 09:00 UTC.
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1, '2024-01-15 10:30:00+02')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (2, '2024-01-15 09:00:00+00')`)
	require.NoError(t, err)

	// Read back as an instant.
	var ts time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT ts FROM t WHERE id = 1`).Scan(&ts))
	assert.Equal(t, time.Date(2024, 1, 15, 8, 30, 0, 0, time.UTC), ts.UTC())

	// Chronological ordering (the offset was applied, not compared as text).
	var first int
	require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM t ORDER BY ts LIMIT 1`).Scan(&first))
	assert.Equal(t, 1, first)

	// A comparison against an offset literal is normalized too.
	var cnt int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM t WHERE ts < '2024-01-15 10:45:00+02'`).Scan(&cnt))
	assert.Equal(t, 1, cnt) // only the 08:30 UTC row is before 08:45 UTC

	// AT TIME ZONE converts the instant to a wall clock in that zone.
	var wall string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT ts AT TIME ZONE 'America/New_York' FROM t WHERE id = 1`).Scan(&wall))
	assert.Equal(t, "2024-01-15 03:30:00", wall) // 08:30 UTC == 03:30 EST
}
