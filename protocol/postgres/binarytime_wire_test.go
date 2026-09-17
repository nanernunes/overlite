package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client asking for binary results gets what the row description promised.
// Time columns were described with their real OID but sent as text, so a
// binary-mode client decoded 27 bytes of "2026-09-17 15:31:54…" as an 8-byte
// integer and failed. pgx asks for binary by default over the extended
// protocol, which is what most Go clients use.
func TestTimeColumnsInBinaryFormat(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (
		id int primary key,
		ts timestamp,
		tstz timestamptz,
		d date)`)
	mustExec(t, conn, `INSERT INTO t VALUES (
		1,
		'2026-09-17 15:31:54',
		'2026-09-17 15:31:54+00',
		'2026-09-17')`)

	var ts, tstz, d time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT ts, tstz, d FROM t WHERE id = 1`).
		Scan(&ts, &tstz, &d))

	assert.Equal(t, 2026, ts.Year())
	assert.Equal(t, time.September, ts.Month())
	assert.Equal(t, 17, ts.Day())
	assert.Equal(t, 15, ts.Hour())
	assert.Equal(t, 31, ts.Minute())
	assert.Equal(t, 54, ts.Second())

	assert.Equal(t, ts.Unix(), tstz.UTC().Unix())

	assert.Equal(t, 2026, d.Year())
	assert.Equal(t, time.September, d.Month())
	assert.Equal(t, 17, d.Day())

	// A NULL still reads as one.
	mustExec(t, conn, `INSERT INTO t (id) VALUES (2)`)
	var nullTS *time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT ts FROM t WHERE id = 2`).Scan(&nullTS))
	assert.Nil(t, nullTS)

	// And a value that came from now() round-trips.
	mustExec(t, conn, `INSERT INTO t (id, tstz) VALUES (3, now())`)
	var fromNow time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT tstz FROM t WHERE id = 3`).Scan(&fromNow))
	assert.WithinDuration(t, time.Now(), fromNow, 24*time.Hour)
}
