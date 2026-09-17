package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// A statement with nothing executable in it used to reach the engine, where the
// SQLite driver answers with a nil Result and the counter call panicked — ending
// the process and every other session with it. database/sql's Ping sends exactly
// such a statement (`-- ping`), so this hit every pgx-backed client on connect.

func TestCommentOnlyStatementsAreEmptyQueries(t *testing.T) {
	addr := startServer(t)
	conn := connect(t, addr)
	ctx := context.Background()

	for _, sql := range []string{"-- ping", "--", "/* c */", "-- ping;", "/* outer /* inner */ */"} {
		_, err := conn.Exec(ctx, sql)
		require.NoErrorf(t, err, "exec %q", sql)

		// The session survives and keeps working.
		var n int
		require.NoErrorf(t, conn.QueryRow(ctx, `SELECT 1`).Scan(&n), "after %q", sql)
		assert.Equal(t, 1, n)
	}
}

func TestCommentOnlyStatementExtendedProtocol(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "-- ping")
	require.NoError(t, err)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestDatabaseSQLPing is the regression this is really about: database/sql
// probes a connection with `-- ping`, so opening a pool used to kill the server.
func TestDatabaseSQLPing(t *testing.T) {
	addr := startServer(t)

	db, err := sql.Open("pgx", fmt.Sprintf("postgres://overlite@%s/main?sslmode=disable", addr))
	require.NoError(t, err)
	defer db.Close()

	require.NoError(t, db.Ping(), "database/sql Ping")

	var n int
	require.NoError(t, db.QueryRow(`SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)

	// A second ping on a pooled connection still works.
	require.NoError(t, db.Ping())
}
