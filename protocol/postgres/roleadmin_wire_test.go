package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCreateRolePrivilege: only a role with CREATEROLE (or a superuser) may
// CREATE/DROP roles; the creator gets ADMIN OPTION on what it creates.
func TestCreateRolePrivilege(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN CREATEROLE PASSWORD 'a'`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN PASSWORD 'b'`)

	alice := connectAs(t, addr, "alice", "a")
	bob := connectAs(t, addr, "bob", "b")

	// alice has CREATEROLE.
	_, err := alice.Exec(ctx, `CREATE ROLE r1`)
	require.NoError(t, err)

	// bob does not.
	_, err = bob.Exec(ctx, `CREATE ROLE r2`)
	require.Error(t, err, "no CREATEROLE")
	_, err = bob.Exec(ctx, `DROP ROLE alice`)
	require.Error(t, err, "no CREATEROLE")

	// The creator gets ADMIN OPTION on r1, so alice may grant it on.
	_, err = alice.Exec(ctx, `GRANT r1 TO bob`)
	require.NoError(t, err, "creator holds admin option")
}

// TestGrantAdminOption: membership can only be granted by a superuser or a role
// holding ADMIN OPTION on the target role; WITH ADMIN OPTION delegates that.
func TestGrantAdminOption(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()

	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN CREATEROLE PASSWORD 'a'`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN PASSWORD 'b'`)
	mustExec(t, admin, `CREATE ROLE team`)
	mustExec(t, admin, `CREATE TABLE t (id int)`)
	mustExec(t, admin, `INSERT INTO t VALUES (1)`)
	mustExec(t, admin, `GRANT SELECT ON t TO team`)

	alice := connectAs(t, addr, "alice", "a")

	// alice has CREATEROLE but no ADMIN OPTION on team (postgres owns it).
	_, err := alice.Exec(ctx, `GRANT team TO bob`)
	require.Error(t, err, "no admin option on team")

	// Delegate admin option to alice; now she can grant team.
	mustExec(t, admin, `GRANT team TO alice WITH ADMIN OPTION`)
	_, err = alice.Exec(ctx, `GRANT team TO bob`)
	require.NoError(t, err)

	// bob now inherits team's SELECT.
	bob := connectAs(t, addr, "bob", "b")
	var n int
	require.NoError(t, bob.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)

	// alice can revoke too (admin option covers it).
	mustExec(t, alice, `REVOKE team FROM bob`)
	require.Error(t, bob.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))

	// A plain member (no admin option) can't grant membership on.
	mustExec(t, admin, `CREATE ROLE carol LOGIN PASSWORD 'c'`)
	mustExec(t, admin, `GRANT team TO carol`) // no WITH ADMIN OPTION
	carol := connectAs(t, addr, "carol", "c")
	_, err = carol.Exec(ctx, `GRANT team TO bob`)
	require.Error(t, err, "plain member lacks admin option")
}
