package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A client parses a value according to the type the column announced, and
// rejects anything that is not that type's text format. A Go driver binding a
// time.Time sends nanoseconds and a trailing Z, which used to be stored and
// handed back untouched: DBeaver's JDBC driver answered "Trailing junk on
// timestamp: 'Z'" and refused to show the row.

func TestTimestampTextIsPostgresShaped(t *testing.T) {
	addr := startServer(t)
	ctx := context.Background()
	// Writes go through bound parameters, the way a driver sends them; reads
	// come back as text, the way a JDBC client asks for them.
	conn := connectExtended(t, addr)
	reader := connect(t, addr)

	mustExec(t, conn, `CREATE TABLE t (id int primary key, ts timestamptz, plain timestamp, d date)`)

	// Exactly what a Go driver sends for a time.Time: RFC3339 with nanoseconds.
	_, err := conn.Exec(ctx, `INSERT INTO t VALUES ($1, $2, $3, $4)`,
		1, "2026-09-17 17:19:19.398458334Z", "2026-09-17 17:19:19.398458334", "2026-09-17")
	require.NoError(t, err)

	var ts, plain, d string
	require.NoError(t, reader.QueryRow(ctx,
		`SELECT ts, plain, d FROM t WHERE id = 1`).Scan(&ts, &plain, &d))

	assert.Equal(t, "2026-09-17 17:19:19.398458+00", ts)
	assert.Equal(t, "2026-09-17 17:19:19.398458", plain)
	assert.Equal(t, "2026-09-17", d)

	for _, v := range []string{ts, plain} {
		assert.NotContains(t, v, "Z", "a trailing Z is not a Postgres timestamp")
		assert.NotContains(t, v, "T", "Postgres separates date and time with a space")
	}
}

// A value written with an offset other than UTC is stored as the same instant.
func TestTimestampWithOffsetParameter(t *testing.T) {
	addr := startServer(t)
	ctx := context.Background()
	conn := connectExtended(t, addr)

	mustExec(t, conn, `CREATE TABLE t (id int primary key, ts timestamptz)`)
	_, err := conn.Exec(ctx, `INSERT INTO t VALUES ($1, $2)`, 1, "2026-09-17 14:19:19-03:00")
	require.NoError(t, err)

	var ts string
	require.NoError(t, connect(t, addr).QueryRow(ctx, `SELECT ts FROM t`).Scan(&ts))
	assert.Equal(t, "2026-09-17 17:19:19+00", ts)
}

// A row already carrying the old shape still reads back correctly: the fix has
// to reach data written before it, not just new writes.
func TestStoredNanosecondTimestampReadsBack(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, ts timestamptz)`)
	// Write it the way it used to be stored, past the normalizing paths.
	mustExec(t, conn, `INSERT INTO t (id) VALUES (1)`)
	mustExec(t, conn, `UPDATE t SET ts = '2026-09-17 17:19:19.398458334Z' WHERE id = 1`)

	var raw string
	require.NoError(t, conn.QueryRow(ctx, `SELECT CAST(ts AS TEXT) FROM t`).Scan(&raw))

	var ts string
	require.NoError(t, conn.QueryRow(ctx, `SELECT ts FROM t`).Scan(&ts))
	assert.Equal(t, "2026-09-17 17:19:19.398458+00", ts,
		"a row stored in the old shape (raw %q) must still read back as a timestamp", raw)
}

// And the value still means the same instant to a client that reads it as one.
func TestTimestampRoundTripsThroughGo(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, ts timestamptz)`)
	want := time.Date(2026, 9, 17, 17, 19, 19, 398458000, time.UTC)
	_, err := conn.Exec(ctx, `INSERT INTO t VALUES ($1, $2)`, 1, want)
	require.NoError(t, err)

	var got time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT ts FROM t`).Scan(&got))
	assert.True(t, want.Equal(got), "got %v, want %v", got, want)
}

// A column's type is what a client reads to decide how to parse its values, so
// timestamptz and timestamp have to be told apart: the catalog reported both as
// "timestamp without time zone" while the wire announced timestamptz for one of
// them, leaving a tool to parse an offset it had been told would not be there.
func TestTimestamptzIsItsOwnType(t *testing.T) {
	addr := startServer(t)
	conn := connect(t, addr)
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (withzone timestamptz, without timestamp, d date)`)

	types := func(column string) (dataType, udtName string) {
		require.NoError(t, conn.QueryRow(ctx, `
			SELECT data_type, udt_name FROM information_schema.columns
			WHERE table_name = 't' AND column_name = $1`, column).Scan(&dataType, &udtName))
		return
	}

	dt, udt := types("withzone")
	assert.Equal(t, "timestamp with time zone", dt)
	assert.Equal(t, "timestamptz", udt)

	dt, udt = types("without")
	assert.Equal(t, "timestamp without time zone", dt)
	assert.Equal(t, "timestamp", udt)

	dt, _ = types("d")
	assert.Equal(t, "date", dt)

	// And the type the catalog reports is the one the wire announces.
	rows, err := conn.Query(ctx, `SELECT withzone, without FROM t`)
	require.NoError(t, err)
	fds := rows.FieldDescriptions()
	require.Len(t, fds, 2)
	assert.Equal(t, uint32(1184), fds[0].DataTypeOID, "timestamptz column")
	assert.Equal(t, uint32(1114), fds[1].DataTypeOID, "timestamp column")
	rows.Close()
}
