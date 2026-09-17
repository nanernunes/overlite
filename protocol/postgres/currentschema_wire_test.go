package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// current_schema() must follow the session's search_path. It used to answer a
// constant "public", which misreports the schema a client works in: gorm's
// Migrator filters information_schema by CURRENT_SCHEMA(), so it asked about
// public and concluded every table in the real schema was missing.

func TestCurrentSchemaFollowsSearchPath(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var schema string
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	assert.Equal(t, "public", schema, "the default path is public")

	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `SET search_path TO app`)

	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	assert.Equal(t, "app", schema)

	// The parenthesis-less spelling Postgres allows works too.
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schema`).Scan(&schema))
	assert.Equal(t, "app", schema)

	// A path naming a schema that does not exist falls back to public.
	mustExec(t, conn, `SET search_path TO nope`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	assert.Equal(t, "public", schema)

	mustExec(t, conn, `RESET search_path`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema))
	assert.Equal(t, "public", schema)
}

// The catalog filter clients actually write: information_schema narrowed by
// CURRENT_SCHEMA(), which is how gorm's HasTable asks.
func TestInformationSchemaByCurrentSchema(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `CREATE TABLE app.widget (id int primary key)`)
	mustExec(t, conn, `SET search_path TO app`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'widget'`).Scan(&n))
	assert.Equal(t, 1, n, "the table was not found in the session's own schema")

	// A table in another schema is not reported. (It has to be created
	// explicitly in public: with the path set, a bare CREATE goes to app.)
	mustExec(t, conn, `CREATE TABLE public.public_only (id int primary key)`)
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = CURRENT_SCHEMA() AND table_name = 'public_only'`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestCurrentSchemas(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `SET search_path TO app`)

	var schemas string
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schemas(false)`).Scan(&schemas))
	assert.Equal(t, "{app,public}", schemas)

	require.NoError(t, conn.QueryRow(ctx, `SELECT current_schemas(true)`).Scan(&schemas))
	assert.Equal(t, "{pg_catalog,app,public}", schemas)
}

// A string that merely looks like a call is left alone.
func TestCurrentSchemaInStringLiteral(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'current_schema()'`).Scan(&s))
	assert.Equal(t, "current_schema()", s)
}

// An explicit public qualifier names the public table even when search_path
// points elsewhere. The qualifier used to be stripped before resolution ran,
// which turned it into a bare name that the path then moved into its own
// schema — a CREATE aimed at public silently landed in another schema.
func TestExplicitPublicQualifierBeatsSearchPath(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA app`)
	mustExec(t, conn, `SET search_path TO app`)
	mustExec(t, conn, `CREATE TABLE public.only_public (id int primary key)`)
	mustExec(t, conn, `INSERT INTO public.only_public VALUES (1)`)

	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'only_public'`).Scan(&schema))
	assert.Equal(t, "public", schema, "the table landed outside public")

	// Reading it back through the qualifier works too.
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM public.only_public`).Scan(&n))
	assert.Equal(t, 1, n)

	// And the unqualified name still resolves through the path, not to public.
	mustExec(t, conn, `CREATE TABLE only_public (id int primary key)`)
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name = 'only_public'`).Scan(&n))
	assert.Equal(t, 2, n, "the path's schema should now have its own copy")
}
