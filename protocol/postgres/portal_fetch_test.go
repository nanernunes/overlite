package postgres_test

import (
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPortalPartialFetch drives the extended protocol at the wire level (pgx's
// high-level API doesn't expose Execute's max-rows) to verify partial fetch:
// Execute with MaxRows=2 returns two rows and PortalSuspended, and the next
// Execute continues from where it left off.
func TestPortalPartialFetch(t *testing.T) {
	addr := startServer(t)
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer conn.Close()
	fe := pgproto3.NewFrontend(conn, conn)

	// Startup + trust auth (no password in tests) up to the first ReadyForQuery.
	fe.Send(&pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters: map[string]string{"user": "postgres", "database": "test"}})
	require.NoError(t, fe.Flush())
	waitFor[*pgproto3.ReadyForQuery](t, fe)

	// Seed 5 rows via the simple query protocol.
	fe.Send(&pgproto3.Query{String: `CREATE TABLE n (i int); INSERT INTO n VALUES (1),(2),(3),(4),(5)`})
	require.NoError(t, fe.Flush())
	waitFor[*pgproto3.ReadyForQuery](t, fe)

	// Extended: parse/bind/describe, then fetch in pages of 2.
	fe.Send(&pgproto3.Parse{Query: `SELECT i FROM n ORDER BY i`})
	fe.Send(&pgproto3.Bind{})
	fe.Send(&pgproto3.Describe{ObjectType: 'P'})
	fe.Send(&pgproto3.Execute{MaxRows: 2})
	fe.Send(&pgproto3.Sync{})
	require.NoError(t, fe.Flush())

	got, suspended := collectExecute(t, fe)
	assert.Equal(t, []string{"1", "2"}, got)
	assert.True(t, suspended, "first page should suspend with more rows pending")
	waitFor[*pgproto3.ReadyForQuery](t, fe)

	// Second page: 2 more rows, still suspended.
	fe.Send(&pgproto3.Execute{MaxRows: 2})
	fe.Send(&pgproto3.Sync{})
	require.NoError(t, fe.Flush())
	got, suspended = collectExecute(t, fe)
	assert.Equal(t, []string{"3", "4"}, got)
	assert.True(t, suspended)
	waitFor[*pgproto3.ReadyForQuery](t, fe)

	// Final page: the last row, then CommandComplete (not suspended).
	fe.Send(&pgproto3.Execute{MaxRows: 2})
	fe.Send(&pgproto3.Sync{})
	require.NoError(t, fe.Flush())
	got, suspended = collectExecute(t, fe)
	assert.Equal(t, []string{"5"}, got)
	assert.False(t, suspended, "last page should complete, not suspend")
}

// collectExecute reads DataRows until either PortalSuspended or CommandComplete.
func collectExecute(t *testing.T, fe *pgproto3.Frontend) (rows []string, suspended bool) {
	t.Helper()
	for {
		msg, err := fe.Receive()
		require.NoError(t, err)
		switch m := msg.(type) {
		case *pgproto3.DataRow:
			rows = append(rows, string(m.Values[0]))
		case *pgproto3.PortalSuspended:
			return rows, true
		case *pgproto3.CommandComplete:
			return rows, false
		case *pgproto3.RowDescription, *pgproto3.ParseComplete, *pgproto3.BindComplete:
			// setup replies; ignore
		default:
			t.Fatalf("unexpected message %T", msg)
		}
	}
}

// waitFor reads backend messages until one of type T arrives.
func waitFor[T pgproto3.BackendMessage](t *testing.T, fe *pgproto3.Frontend) T {
	t.Helper()
	for {
		msg, err := fe.Receive()
		require.NoError(t, err)
		if m, ok := msg.(T); ok {
			return m
		}
	}
}
