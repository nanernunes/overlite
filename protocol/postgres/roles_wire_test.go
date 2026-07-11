package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRolesLifecycle(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	rolenames := func() []string {
		return queryColumn(t, conn, `SELECT rolname FROM pg_catalog.pg_roles ORDER BY rolname`, 0)
	}

	// The default role exists.
	assert.Contains(t, rolenames(), "postgres")

	// CREATE ROLE / CREATE USER (with attributes) succeed and show up.
	mustExec(t, conn, `CREATE ROLE app_reader`)
	mustExec(t, conn, `CREATE USER webuser WITH PASSWORD 'secret' CREATEDB`)
	roles := rolenames()
	assert.Contains(t, roles, "app_reader")
	assert.Contains(t, roles, "webuser")

	// Attributes are reflected: USER logs in, CREATEDB set; plain ROLE cannot log in.
	var login, createdb bool
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT rolcanlogin, rolcreatedb FROM pg_catalog.pg_roles WHERE rolname = 'webuser'`).Scan(&login, &createdb))
	assert.True(t, login)
	assert.True(t, createdb)
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'app_reader'`).Scan(&login))
	assert.False(t, login)

	// GRANT / REVOKE succeed as no-ops (even against a table that doesn't exist).
	_, err := conn.Exec(ctx, `GRANT SELECT ON some_table TO app_reader`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `REVOKE ALL ON some_table FROM app_reader`)
	require.NoError(t, err)

	// ALTER ROLE updates only the named attribute.
	mustExec(t, conn, `ALTER ROLE app_reader WITH LOGIN`)
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT rolcanlogin FROM pg_catalog.pg_roles WHERE rolname = 'app_reader'`).Scan(&login))
	assert.True(t, login)

	// DROP ROLE removes it.
	mustExec(t, conn, `DROP ROLE app_reader`)
	assert.NotContains(t, rolenames(), "app_reader")

	// The internal roles table is hidden from the catalog.
	for _, rel := range queryColumn(t, conn,
		`SELECT relname FROM pg_catalog.pg_class WHERE relkind = 'r'`, 0) {
		assert.False(t, strings.HasPrefix(rel, "_overlite"), "internal table %q leaked into catalog", rel)
	}
}
