package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRoleMembership: GRANT role TO role confers the role's privileges to its
// members (default INHERIT), transitively, and REVOKE takes them away.
func TestRoleMembership(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE reader`)
	mustExec(t, admin, `CREATE ROLE staff`) // staff will be a member of reader
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `CREATE TABLE t (id int)`)
	mustExec(t, admin, `INSERT INTO t VALUES (1)`)
	mustExec(t, admin, `GRANT SELECT ON t TO reader`)

	alice := connectAs(t, addr, "alice", "a")

	// Not a member yet -> denied.
	var n int
	require.Error(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))

	// Direct membership: alice inherits reader's SELECT.
	mustExec(t, admin, `GRANT reader TO alice`)
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
	_, err := alice.Exec(ctx, `INSERT INTO t VALUES (2)`)
	require.Error(t, err, "reader only has SELECT")

	// REVOKE removes the inheritance.
	mustExec(t, admin, `REVOKE reader FROM alice`)
	require.Error(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))

	// Transitive: alice -> staff -> reader.
	mustExec(t, admin, `GRANT reader TO staff`)
	mustExec(t, admin, `GRANT staff TO alice`)
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestRoleMembershipNoInherit: a NOINHERIT member doesn't get privileges
// automatically, but can still reach them by SET ROLE.
func TestRoleMembershipNoInherit(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE reader`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN NOINHERIT PASSWORD 'b'`)
	mustExec(t, admin, `CREATE TABLE t (id int)`)
	mustExec(t, admin, `INSERT INTO t VALUES (1)`)
	mustExec(t, admin, `GRANT SELECT ON t TO reader`)
	mustExec(t, admin, `GRANT reader TO bob`)

	bob := connectAs(t, addr, "bob", "b")

	// NOINHERIT: no automatic privilege from reader.
	var n int
	require.Error(t, bob.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))

	// But after SET ROLE reader, the privilege applies.
	_, err := bob.Exec(ctx, `SET ROLE reader`)
	require.NoError(t, err)
	require.NoError(t, bob.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
}
