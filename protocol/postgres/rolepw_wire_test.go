package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialUser connects as a specific role/password (sslmode disabled).
func dialUser(t *testing.T, addr, user, pw string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx,
		fmt.Sprintf("postgres://%s:%s@%s/test?sslmode=disable", user, pw, addr))
	if err != nil {
		return err
	}
	conn.Close(context.Background())
	return nil
}

// TestPerRolePassword: a role created with its own password authenticates
// against that password, independent of POSTGRES_PASSWORD (the default role).
func TestPerRolePassword(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw") // enables password auth (scram default)
	addr := startServer(t)

	// The default role connects with the global password and creates a role.
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'alicepw'`)

	// alice authenticates with her own password.
	assert.NoError(t, dialUser(t, addr, "alice", "alicepw"), "alice's password should work")
	assert.Error(t, dialUser(t, addr, "alice", "wrong"), "wrong password rejected")

	// The default role still uses the global password.
	assert.NoError(t, dialUser(t, addr, "postgres", "adminpw"))
	assert.Error(t, dialUser(t, addr, "postgres", "nope"))

	// ALTER ROLE changes the stored password.
	mustExec(t, admin, `ALTER ROLE alice PASSWORD 'newpw'`)
	assert.Error(t, dialUser(t, addr, "alice", "alicepw"), "old password no longer works")
	assert.NoError(t, dialUser(t, addr, "alice", "newpw"), "new password works")
}

// connectAs returns a pgx connection as the given role/password.
func connectAs(t *testing.T, addr, user, pw string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx,
		fmt.Sprintf("postgres://%s:%s@%s/test?sslmode=disable", user, pw, addr))
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}
