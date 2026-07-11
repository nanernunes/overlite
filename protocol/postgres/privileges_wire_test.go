package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPrivilegeEnforcement covers GRANT/REVOKE enforcement for a non-superuser
// role: no access without a grant, access after GRANT, denial after REVOKE,
// per-privilege granularity, owner access, and GRANT ... TO PUBLIC.
func TestPrivilegeEnforcement(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw") // seeded superuser
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN PASSWORD 'b'`)
	mustExec(t, admin, `CREATE TABLE t (id int)`) // owned by postgres
	mustExec(t, admin, `INSERT INTO t VALUES (1)`)

	alice := connectAs(t, addr, "alice", "a")

	// No grant: alice can't read t.
	var n int
	err := alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n)
	require.Error(t, err, "no privilege -> denied")

	// GRANT SELECT lets her read but not write.
	mustExec(t, admin, `GRANT SELECT ON t TO alice`)
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
	_, err = alice.Exec(ctx, `INSERT INTO t VALUES (2)`)
	require.Error(t, err, "only SELECT granted -> INSERT denied")

	// REVOKE removes it again.
	mustExec(t, admin, `REVOKE SELECT ON t FROM alice`)
	require.Error(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))

	// Ownership: bob creates and uses his own table.
	bob := connectAs(t, addr, "bob", "b")
	mustExec(t, bob, `CREATE TABLE bt (x int)`)
	_, err = bob.Exec(ctx, `INSERT INTO bt VALUES (5)`)
	require.NoError(t, err, "owner has all privileges")

	// alice can't touch bob's table until a PUBLIC grant.
	require.Error(t, alice.QueryRow(ctx, `SELECT count(*) FROM bt`).Scan(&n))
	mustExec(t, bob, `GRANT SELECT ON bt TO PUBLIC`)
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM bt`).Scan(&n))
	assert.Equal(t, 1, n)

	// Superuser bypasses all checks.
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM bt`).Scan(&n))
}

// TestGrantAllAndDrop covers GRANT ALL and owner-only DROP.
func TestGrantAllAndDrop(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `CREATE TABLE t (id int)`)

	alice := connectAs(t, addr, "alice", "a")

	// GRANT ALL: alice can insert and select.
	mustExec(t, admin, `GRANT ALL ON t TO alice`)
	_, err := alice.Exec(ctx, `INSERT INTO t VALUES (1)`)
	require.NoError(t, err)
	var n int
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)

	// But she can't DROP a table she doesn't own (grants don't confer DROP).
	_, err = alice.Exec(ctx, `DROP TABLE t`)
	require.Error(t, err, "only the owner drops a table")
}
