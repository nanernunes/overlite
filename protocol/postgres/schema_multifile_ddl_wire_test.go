package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// In multi-file mode a schema is an attached database, and SQLite spells two
// pieces of DDL differently there: it qualifies the index rather than the
// table, and it cannot reference across databases at all.

func TestQualifiedCreateIndexInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, total int, label text)`)

	// The Postgres spelling: the index is bare, the table is qualified.
	mustExec(t, conn, `CREATE INDEX idx_orders_total ON sales.orders (total)`)
	mustExec(t, conn, `CREATE INDEX "idx_orders_label" ON "sales"."orders" ("label")`)
	mustExec(t, conn, `CREATE UNIQUE INDEX idx_orders_uniq ON sales.orders (total, label)`)

	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE schemaname = 'sales' AND tablename = 'orders'`).Scan(&n))
	assert.Equal(t, 3, n)

	// The index actually works.
	mustExec(t, conn, `INSERT INTO sales.orders VALUES (1, 10, 'a')`)
	_, err := conn.Exec(ctx, `INSERT INTO sales.orders VALUES (2, 10, 'a')`)
	require.Error(t, err, "the unique index is not enforced")
}

func TestQualifiedForeignKeyInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."customers" (id int primary key)`)

	// A reference to the table's own schema is the common case and works.
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (
		id int primary key,
		customer_id int REFERENCES "sales"."customers"("id"))`)

	mustExec(t, conn, `INSERT INTO sales.customers VALUES (1)`)
	mustExec(t, conn, `INSERT INTO sales.orders VALUES (10, 1)`)

	_, err := conn.Exec(ctx, `INSERT INTO sales.orders VALUES (11, 999)`)
	require.Error(t, err, "the foreign key is not enforced")
}

// A reference across schemas cannot work when each is its own database file.
// Saying so is better than the syntax error SQLite would raise.
func TestCrossSchemaForeignKeyIsReportedInMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE SCHEMA billing`)
	mustExec(t, conn, `CREATE TABLE "billing"."accounts" (id int primary key)`)

	_, err := conn.Exec(ctx, `CREATE TABLE "sales"."orders" (
		id int primary key,
		account_id int REFERENCES "billing"."accounts"("id"))`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "multi-file schema mode")
}

// Multi-file mode keeps each schema in its own database file, and SQLite caps
// how many it will attach. The cap is reached quickly, so the message has to
// name the cause and the way out rather than pass SQLite's along.
func TestAttachLimitIsExplained(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var lastErr error
	for i := 0; i < 20 && lastErr == nil; i++ {
		_, lastErr = conn.Exec(ctx, fmt.Sprintf("CREATE SCHEMA s%02d", i))
	}
	require.Error(t, lastErr, "the attach limit was never reached")
	assert.Contains(t, lastErr.Error(), "single-file mode")
	assert.Contains(t, lastErr.Error(), "own database file")
}
