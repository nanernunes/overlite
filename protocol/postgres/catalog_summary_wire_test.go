package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pg_tables, pg_indexes and pg_views are the readable summaries psql and most
// migration tools reach for before the pg_class joins. to_regclass is the cheap
// existence check. All four were missing.

func TestPgTablesAndIndexes(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE orders (id int primary key, total int)`)
	mustExec(t, conn, `CREATE INDEX idx_orders_total ON orders (total)`)
	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE sales.invoices (id int primary key)`)

	var schema, owner string
	var hasIndexes bool
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT schemaname, tableowner, hasindexes FROM pg_tables WHERE tablename = 'orders'`).
		Scan(&schema, &owner, &hasIndexes))
	assert.Equal(t, "public", schema)
	assert.NotEmpty(t, owner)
	assert.True(t, hasIndexes)

	// A table in another schema is reported under that schema.
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT schemaname FROM pg_tables WHERE tablename = 'invoices'`).Scan(&schema))
	assert.Equal(t, "sales", schema)

	// Internal tables stay hidden.
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_tables WHERE tablename LIKE '_overlite%'`).Scan(&n))
	assert.Equal(t, 0, n)

	var indexdef string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT schemaname, indexdef FROM pg_indexes WHERE indexname = 'idx_orders_total'`).
		Scan(&schema, &indexdef))
	assert.Equal(t, "public", schema)
	assert.Contains(t, indexdef, "idx_orders_total")
}

func TestPgViews(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key)`)
	mustExec(t, conn, `CREATE VIEW v AS SELECT id FROM t`)

	var schema, definition string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT schemaname, definition FROM pg_views WHERE viewname = 'v'`).Scan(&schema, &definition))
	assert.Equal(t, "public", schema)
	assert.Contains(t, definition, "SELECT")
}

func TestToRegclass(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE orders (id int primary key)`)
	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE sales.invoices (id int primary key)`)

	// The oid itself is opaque; what clients do with it is compare it to NULL.
	exists := func(name string) bool {
		var oid any
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)`, name).Scan(&oid))
		return oid != nil
	}

	assert.True(t, exists("orders"))
	assert.False(t, exists("nope"), "a missing relation should resolve to NULL")
	assert.True(t, exists("sales.invoices"), "a qualified name should resolve")
	assert.False(t, exists("sales.nope"))
	// A relation in another schema is not found under the wrong one.
	assert.False(t, exists("public.invoices"))
}

// gorm's AutoMigrate reads this column when it inspects an existing table; it
// was missing, so the migration failed on the second run.
func TestInformationSchemaIdentityColumns(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id serial primary key, name text)`)

	rows, err := conn.Query(ctx, `
		SELECT column_name, identity_increment, identity_generation, generation_expression
		FROM information_schema.columns WHERE table_name = 't'`)
	require.NoError(t, err)
	defer rows.Close()

	seen := 0
	for rows.Next() {
		seen++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 2, seen)
}

// to_regclass with a bound parameter, which is what the extended protocol
// sends. Resolving only literals would answer NULL for every such call — a
// wrong answer rather than an error, so nothing would notice.
func TestToRegclassParameterized(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE orders (id int primary key)`)
	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE sales.invoices (id int primary key)`)

	exists := func(name string) bool {
		var oid any
		require.NoError(t, conn.QueryRow(ctx, `SELECT to_regclass($1)`, name).Scan(&oid))
		return oid != nil
	}

	assert.True(t, exists("orders"))
	assert.False(t, exists("nope"))
	assert.True(t, exists("sales.invoices"))
	assert.False(t, exists("sales.nope"))
	assert.False(t, exists("public.invoices"))
}

// information_schema names the database the client sees. Reporting SQLite's
// internal "main" made every catalog query filtered by catalog come back
// empty — which is how gorm's migrator asks what columns a table already has,
// so it concluded there were none and tried to add them all again.
func TestCatalogNamesTheClientDatabase(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, label text)`)

	var dbName string
	require.NoError(t, conn.QueryRow(ctx, `SELECT current_database()`).Scan(&dbName))

	var catalog string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_catalog FROM information_schema.tables WHERE table_name = 't'`).Scan(&catalog))
	assert.Equal(t, dbName, catalog, "information_schema.tables names another database")

	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_catalog FROM information_schema.columns WHERE table_name = 't' LIMIT 1`).Scan(&catalog))
	assert.Equal(t, dbName, catalog, "information_schema.columns names another database")

	// The filter a migrator writes finds the columns.
	var n int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.columns
		WHERE table_catalog = current_database()
		  AND table_schema = CURRENT_SCHEMA()
		  AND table_name = 't'`).Scan(&n))
	assert.Equal(t, 2, n)
}
