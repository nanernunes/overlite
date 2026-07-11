package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPgDependPopulated: real structural dependencies (index/constraint/trigger/
// policy → their table) show up in pg_depend, with oids that resolve through the
// other catalogs.
func TestPgDependPopulated(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE TABLE t (id int PRIMARY KEY, x int)`)
	mustExec(t, admin, `CREATE INDEX ix ON t (x)`)
	mustExec(t, admin, `ALTER TABLE t ENABLE ROW LEVEL SECURITY`)
	mustExec(t, admin, `CREATE POLICY p ON t FOR SELECT USING (id > 0)`)

	// The index depends on its table.
	var n int
	require.NoError(t, admin.QueryRow(ctx, `
		SELECT count(*) FROM pg_depend d
		JOIN pg_class idx ON idx.oid = d.objid
		JOIN pg_class tbl ON tbl.oid = d.refobjid
		WHERE idx.relname = 'ix' AND tbl.relname = 't' AND d.deptype = 'a'`).Scan(&n))
	assert.Equal(t, 1, n, "index -> table dependency")

	// The policy depends on its table (classid = pg_policy).
	require.NoError(t, admin.QueryRow(ctx, `
		SELECT count(*) FROM pg_depend d
		JOIN pg_class tbl ON tbl.oid = d.refobjid
		WHERE d.classid = 3256 AND tbl.relname = 't'`).Scan(&n))
	assert.Equal(t, 1, n, "policy -> table dependency")
}

// TestPgShdependPopulated: table ownership shows up in pg_shdepend (the shared
// dependency of an object on its owning role).
func TestPgShdependPopulated(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE TABLE owned (id int)`)

	var role string
	require.NoError(t, admin.QueryRow(ctx, `
		SELECT r.rolname FROM pg_shdepend sd
		JOIN pg_class c ON c.oid = sd.objid
		JOIN pg_roles r ON r.oid = sd.refobjid
		WHERE c.relname = 'owned' AND sd.deptype = 'o'`).Scan(&role))
	assert.Equal(t, "postgres", role)
}
