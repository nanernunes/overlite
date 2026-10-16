package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A client must get in while another holds a write transaction, as Postgres
// lets it. Each new connection used to write its bootstrap (the default role,
// the internal tables' migrations) and so waited on the writer's lock, past
// the session timeout — refused as "too many clients" with the server empty.
func TestSessionOpensWhileAnotherWrites(t *testing.T) {
	eng, err := Open(filepath.Join(t.TempDir(), "busy.db"))
	require.NoError(t, err)
	t.Cleanup(func() { eng.Close() })
	ctx := context.Background()

	writer, err := eng.Session(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { writer.Close() })
	_, err = writer.Execute(ctx, "CREATE TABLE t (id INTEGER)", nil)
	require.NoError(t, err)
	tx, err := writer.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Execute(ctx, "INSERT INTO t VALUES (1)", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	start := time.Now()
	reader, err := eng.Session(ctx)
	require.NoError(t, err, "a client could not get in while another writes")
	t.Cleanup(func() { reader.Close() })
	require.Less(t, time.Since(start), time.Second, "getting in waited on the writer")

	rs, err := reader.Execute(ctx, "SELECT count(*) FROM t", nil)
	require.NoError(t, err)
	require.Len(t, rs.Rows, 1)
}
