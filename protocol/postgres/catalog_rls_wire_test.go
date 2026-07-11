package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPgAuthMembersPopulated: role membership shows up in pg_auth_members with
// oids that resolve back through pg_roles (this is what psql's \du "Member of"
// reads).
func TestPgAuthMembersPopulated(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE team`)
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `GRANT team TO alice WITH ADMIN OPTION`)

	var member, role string
	var admn int
	err := admin.QueryRow(ctx, `
		SELECT mo.rolname, ro.rolname, m.admin_option
		FROM pg_auth_members m
		JOIN pg_roles ro ON ro.oid = m.roleid
		JOIN pg_roles mo ON mo.oid = m.member
		WHERE ro.rolname = 'team'`).Scan(&member, &role, &admn)
	require.NoError(t, err)
	assert.Equal(t, "alice", member)
	assert.Equal(t, "team", role)
	assert.Equal(t, 1, admn, "granted WITH ADMIN OPTION")
}

// TestPgPolicyPopulated: a CREATE POLICY is visible in pg_policy joined to
// pg_class (what psql's \d reads to list policies).
func TestPgPolicyPopulated(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE TABLE t (id int)`)
	mustExec(t, admin, `ALTER TABLE t ENABLE ROW LEVEL SECURITY`)
	mustExec(t, admin, `CREATE POLICY p ON t FOR SELECT USING (id > 0)`)

	var name, cmd, qual string
	var permissive int
	err := admin.QueryRow(ctx, `
		SELECT pol.polname, pol.polcmd, pol.polpermissive, pol.polqual
		FROM pg_policy pol
		JOIN pg_class c ON c.oid = pol.polrelid
		WHERE c.relname = 't'`).Scan(&name, &cmd, &permissive, &qual)
	require.NoError(t, err)
	assert.Equal(t, "p", name)
	assert.Equal(t, "r", cmd, "FOR SELECT -> 'r'")
	assert.Equal(t, 1, permissive)
	assert.Contains(t, qual, "id > 0")
}
