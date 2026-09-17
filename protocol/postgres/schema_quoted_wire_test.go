package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Most ORMs quote every identifier they emit, so a schema-qualified name
// reaches the server as `"sales"."orders"`. Only the bare spelling used to be
// recognised, which left those clients unable to use schemas at all: the
// quoted form fell through to SQLite, which read it as an attached database
// and answered `unknown database "sales"`.

func TestQuotedSchemaQualifiers(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)

	// Every quoting combination names the same table.
	for i, create := range []string{
		`CREATE TABLE "sales"."t%d" (id int primary key, label text)`,
		`CREATE TABLE sales."t%d" (id int primary key, label text)`,
		`CREATE TABLE "sales".t%d (id int primary key, label text)`,
		`CREATE TABLE sales.t%d (id int primary key, label text)`,
	} {
		_, err := conn.Exec(ctx, fmt.Sprintf(create, i))
		require.NoErrorf(t, err, "create form %d", i)
	}

	mustExec(t, conn, `INSERT INTO "sales"."t0" VALUES (1, 'quoted')`)

	var label string
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM "sales"."t0"`).Scan(&label))
	assert.Equal(t, "quoted", label)

	// The bare and quoted spellings reach the same table.
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM sales.t0`).Scan(&label))
	assert.Equal(t, "quoted", label)

	mustExec(t, conn, `UPDATE "sales"."t0" SET label = 'updated' WHERE id = 1`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM sales.t0`).Scan(&label))
	assert.Equal(t, "updated", label)

	mustExec(t, conn, `DELETE FROM "sales"."t0" WHERE id = 1`)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM sales.t0`).Scan(&n))
	assert.Equal(t, 0, n)
}

// The catalog must see a table created through the quoted spelling as
// belonging to the schema, not to public.
func TestQuotedSchemaQualifierInCatalog(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key)`)

	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'orders'`).Scan(&schema))
	assert.Equal(t, "sales", schema)
}

// A quoted name that is not a schema keeps its own meaning: an alias qualifier
// must not be mistaken for a schema qualifier.
func TestQuotedAliasIsNotASchema(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, total int)`)
	mustExec(t, conn, `INSERT INTO "sales"."orders" VALUES (1, 10)`)

	// "o" is an alias here, not a schema.
	var total int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT "o"."total" FROM "sales"."orders" "o" WHERE "o"."id" = 1`).Scan(&total))
	assert.Equal(t, 10, total)

	// A string literal that merely looks like a qualifier is left alone.
	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'sales.orders'`).Scan(&s))
	assert.Equal(t, "sales.orders", s)
}

// An index on a quoted qualified table belongs to that table's schema.
func TestIndexOnQuotedSchemaTable(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, total int)`)
	mustExec(t, conn, `CREATE INDEX "idx_total" ON "sales"."orders" ("total")`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace ns ON ns.oid = c.relnamespace
		WHERE ns.nspname = 'sales' AND c.relname = 'orders'`).Scan(&n))
	assert.GreaterOrEqual(t, n, 1)
}

// A foreign key written with quoted qualified names is enforced.
func TestQuotedSchemaForeignKey(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."customers" (id int primary key)`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (
		id int primary key,
		customer_id int REFERENCES "sales"."customers"("id"))`)
	mustExec(t, conn, `INSERT INTO "sales"."customers" VALUES (1)`)
	mustExec(t, conn, `INSERT INTO "sales"."orders" VALUES (10, 1)`)

	_, err := conn.Exec(ctx, `INSERT INTO "sales"."orders" VALUES (11, 999)`)
	require.Error(t, err, "a dangling foreign key was accepted")
}
