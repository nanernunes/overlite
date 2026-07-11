package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func scanStr(t *testing.T, conn *pgx.Conn, q string) string {
	t.Helper()
	var s string
	require.NoErrorf(t, conn.QueryRow(context.Background(), q).Scan(&s), "query %q", q)
	return s
}

// TestCurrentUserSimpleProtocol: with no password (trust), the session reflects
// the login role (the test client logs in as "overlite").
func TestCurrentUserSimpleProtocol(t *testing.T) {
	conn := connect(t, startServer(t))
	assert.Equal(t, "overlite", scanStr(t, conn, `SELECT current_user`))
	assert.Equal(t, "overlite", scanStr(t, conn, `SELECT session_user`))
	assert.Equal(t, "overlite", scanStr(t, conn, `SELECT current_user()`))
}

// TestSetRole: current_user follows SET ROLE; session_user stays the login role;
// RESET ROLE restores it; a missing role errors.
func TestSetRole(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'alicepw'`)
	mustExec(t, admin, `CREATE ROLE reporter`)
	mustExec(t, admin, `CREATE ROLE other`)
	mustExec(t, admin, `GRANT reporter TO alice`) // alice may become reporter

	alice := connectAs(t, addr, "alice", "alicepw")
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT current_user`))
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT session_user`))

	// SET ROLE to a role she's a member of changes current_user, not session_user.
	mustExec(t, alice, `SET ROLE reporter`)
	assert.Equal(t, "reporter", scanStr(t, alice, `SELECT current_user`))
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT session_user`))

	// RESET ROLE restores.
	mustExec(t, alice, `RESET ROLE`)
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT current_user`))

	// SET ROLE to a missing role is an error, and identity is unchanged.
	_, err := alice.Exec(ctx, `SET ROLE ghost`)
	require.Error(t, err)
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT current_user`))

	// SET ROLE to a role she is NOT a member of is denied.
	_, err = alice.Exec(ctx, `SET ROLE other`)
	require.Error(t, err, "not a member of other")
	assert.Equal(t, "alice", scanStr(t, alice, `SELECT current_user`))
}

// TestSetSessionAuthorization changes both session_user and current_user.
func TestSetSessionAuthorization(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE svc`)

	mustExec(t, admin, `SET SESSION AUTHORIZATION svc`)
	assert.Equal(t, "svc", scanStr(t, admin, `SELECT session_user`))
	assert.Equal(t, "svc", scanStr(t, admin, `SELECT current_user`))

	mustExec(t, admin, `RESET SESSION AUTHORIZATION`)
	assert.Equal(t, "postgres", scanStr(t, admin, `SELECT session_user`))
}
