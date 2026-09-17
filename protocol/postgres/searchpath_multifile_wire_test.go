package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In multi-file mode a schema is an attached database. search_path used to be
// ignored there, so an unqualified name fell through to SQLite, which resolves
// it by attach order: every session read and wrote whichever tenant happened to
// be attached first, whatever its search_path said. Two tenants with the same
// table name shared one table.

// TestSearchPathIsolatesTenantsInMultiFileMode is the regression that matters:
// a leak here mixes one customer's rows into another's.
func TestSearchPathIsolatesTenantsInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	addr := startServer(t)
	ctx := context.Background()

	setup := connect(t, addr)
	mustExec(t, setup, `CREATE SCHEMA acme`)
	mustExec(t, setup, `CREATE SCHEMA globex`)
	mustExec(t, setup, `CREATE TABLE acme.users (id int primary key, name text)`)
	mustExec(t, setup, `CREATE TABLE globex.users (id int primary key, name text)`)

	// Each tenant writes through an unqualified name, under its own path.
	for _, tenant := range []string{"acme", "globex"} {
		conn := connect(t, addr)
		mustExec(t, conn, `SET search_path TO `+tenant)
		_, err := conn.Exec(ctx, `INSERT INTO users VALUES (1, $1)`, tenant+"-user")
		require.NoErrorf(t, err, "insert as %s", tenant)
	}

	// Each row landed in its own tenant's table.
	for _, tenant := range []string{"acme", "globex"} {
		var name string
		require.NoError(t, setup.QueryRow(ctx,
			`SELECT name FROM `+tenant+`.users WHERE id = 1`).Scan(&name))
		assert.Equalf(t, tenant+"-user", name, "%s.users holds another tenant's row", tenant)

		var n int
		require.NoError(t, setup.QueryRow(ctx,
			`SELECT count(*) FROM `+tenant+`.users`).Scan(&n))
		assert.Equalf(t, 1, n, "%s.users has the wrong number of rows", tenant)
	}

	// And each tenant reads back only its own.
	for _, tenant := range []string{"acme", "globex"} {
		conn := connect(t, addr)
		mustExec(t, conn, `SET search_path TO `+tenant)

		var name string
		require.NoError(t, conn.QueryRow(ctx, `SELECT name FROM users WHERE id = 1`).Scan(&name))
		assert.Equalf(t, tenant+"-user", name, "%s read another tenant's row", tenant)
	}
}

// A path-qualified CREATE lands in the path's first schema, not in public.
func TestSearchPathCreateInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	addr := startServer(t)
	ctx := context.Background()

	conn := connect(t, addr)
	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `SET search_path TO app`)
	mustExec(t, conn, `CREATE TABLE widget (id int primary key)`)
	mustExec(t, conn, `INSERT INTO widget VALUES (1)`)

	other := connect(t, addr)
	var n int
	require.NoError(t, other.QueryRow(ctx, `SELECT count(*) FROM app.widget`).Scan(&n))
	assert.Equal(t, 1, n, "the table was not created in the path's schema")

	// public did not get one.
	var inPublic int
	require.NoError(t, other.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables
		 WHERE table_schema = 'public' AND table_name = 'widget'`).Scan(&inPublic))
	assert.Equal(t, 0, inPublic, "the table leaked into public")
}

// An unqualified name still finds a public table when no path schema has one.
func TestSearchPathFallsBackToPublicInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	addr := startServer(t)
	ctx := context.Background()

	conn := connect(t, addr)
	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `CREATE TABLE shared (id int primary key)`)
	mustExec(t, conn, `INSERT INTO shared VALUES (7)`)
	mustExec(t, conn, `SET search_path TO app, public`)

	var id int
	require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM shared`).Scan(&id))
	assert.Equal(t, 7, id)
}
