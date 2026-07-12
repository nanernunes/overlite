package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoLoginRole: a role marked NOLOGIN can't open a session even with the
// right password; LOGIN restores it, and ALTER ROLE flips it either way.
func TestNoLoginRole(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	admin := connectAs(t, addr, "postgres", "adminpw")

	mustExec(t, admin, `CREATE ROLE svc NOLOGIN PASSWORD 'svcpw'`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN PASSWORD 'bobpw'`)

	// NOLOGIN role is rejected despite the correct password.
	err := dialUser(t, addr, "svc", "svcpw")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not permitted to log in")

	// LOGIN role connects.
	assert.NoError(t, dialUser(t, addr, "bob", "bobpw"))

	// ALTER ROLE flips the flag both ways.
	mustExec(t, admin, `ALTER ROLE bob NOLOGIN`)
	assert.Error(t, dialUser(t, addr, "bob", "bobpw"), "bob is now NOLOGIN")
	mustExec(t, admin, `ALTER ROLE svc LOGIN`)
	assert.NoError(t, dialUser(t, addr, "svc", "svcpw"), "svc is now LOGIN")

	// The default superuser is unaffected.
	assert.NoError(t, dialUser(t, addr, "postgres", "adminpw"))
}
