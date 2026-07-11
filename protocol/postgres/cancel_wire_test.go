package postgres_test

import (
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestQueryCancellation runs a long query on one connection and cancels it from
// a second connection via a CancelRequest carrying the first's backend key. The
// running query must abort with SQLSTATE 57014.
func TestQueryCancellation(t *testing.T) {
	addr := startServer(t)

	// Connection A: start up and capture the backend key.
	connA, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	defer connA.Close()
	feA := pgproto3.NewFrontend(connA, connA)
	feA.Send(&pgproto3.StartupMessage{ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters: map[string]string{"user": "postgres", "database": "test"}})
	require.NoError(t, feA.Flush())

	var pid uint32
	var secret []byte
	for {
		msg, err := feA.Receive()
		require.NoError(t, err)
		if k, ok := msg.(*pgproto3.BackendKeyData); ok {
			pid, secret = k.ProcessID, k.SecretKey
		}
		if _, ok := msg.(*pgproto3.ReadyForQuery); ok {
			break
		}
	}
	require.NotZero(t, pid)

	// A starts a long-running query.
	feA.Send(&pgproto3.Query{String: `WITH RECURSIVE c(x) AS (
		SELECT 1 UNION ALL SELECT x + 1 FROM c WHERE x < 5000000000)
		SELECT count(*) FROM c`})
	require.NoError(t, feA.Flush())

	// Give the server a moment to start executing, then cancel from connection B.
	time.Sleep(200 * time.Millisecond)
	connB, err := net.DialTimeout("tcp", addr, 5*time.Second)
	require.NoError(t, err)
	feB := pgproto3.NewFrontend(connB, connB)
	feB.Send(&pgproto3.CancelRequest{ProcessID: pid, SecretKey: secret})
	require.NoError(t, feB.Flush())
	connB.Close()

	// A must receive an ErrorResponse (57014), not a CommandComplete.
	_ = connA.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		msg, err := feA.Receive()
		require.NoError(t, err, "expected a cancellation error, not a hang/completion")
		if e, ok := msg.(*pgproto3.ErrorResponse); ok {
			assert.Equal(t, "57014", e.Code, "query should be cancelled")
			return
		}
		if _, ok := msg.(*pgproto3.CommandComplete); ok {
			t.Fatal("query completed instead of being cancelled")
		}
	}
}
