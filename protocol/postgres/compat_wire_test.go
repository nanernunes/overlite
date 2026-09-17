package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Postgres-isms that used to fail to parse. Each is a syntax error away from
// working, which is a hard stop for a client that emits it.

func TestILike(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, name text)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 'Printer Jam'), (2, 'vpn down')`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name ILIKE '%printer%'`).Scan(&n))
	assert.Equal(t, 1, n, "ILIKE should match regardless of case")

	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name ILIKE '%VPN%'`).Scan(&n))
	assert.Equal(t, 1, n)

	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE name NOT ILIKE '%printer%'`).Scan(&n))
	assert.Equal(t, 1, n)

	// A literal containing the word is not an operator.
	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT 'ilike'`).Scan(&s))
	assert.Equal(t, "ilike", s)
}

func TestSelectForUpdate(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, n int)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 10)`)

	for _, sql := range []string{
		`SELECT n FROM t WHERE id = 1 FOR UPDATE`,
		`SELECT n FROM t WHERE id = 1 FOR NO KEY UPDATE`,
		`SELECT n FROM t WHERE id = 1 FOR SHARE`,
		`SELECT n FROM t WHERE id = 1 FOR KEY SHARE`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE NOWAIT`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE SKIP LOCKED`,
		`SELECT n FROM t WHERE id = 1 FOR UPDATE OF t`,
	} {
		var n int
		require.NoErrorf(t, conn.QueryRow(ctx, sql).Scan(&n), "%s", sql)
		assert.Equal(t, 10, n)
	}

	// A counter read under FOR UPDATE, the usual reason to write one.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	var n int
	require.NoError(t, tx.QueryRow(ctx, `SELECT n FROM t WHERE id = 1 FOR UPDATE`).Scan(&n))
	_, err = tx.Exec(ctx, `UPDATE t SET n = $1 WHERE id = 1`, n+1)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	require.NoError(t, conn.QueryRow(ctx, `SELECT n FROM t WHERE id = 1`).Scan(&n))
	assert.Equal(t, 11, n)
}

func TestComputedColumnDefaults(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (
		id uuid DEFAULT gen_random_uuid(),
		created_at timestamptz DEFAULT now(),
		label text DEFAULT 'none',
		n int DEFAULT 0)`)
	mustExec(t, conn, `INSERT INTO t (label) VALUES ('x')`)

	var id, createdAt, label string
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT id, created_at, label, n FROM t`).Scan(&id, &createdAt, &label, &n))
	assert.Len(t, id, 36, "the uuid default did not run")
	assert.NotEmpty(t, createdAt, "the now() default did not run")
	assert.Equal(t, "x", label)
	assert.Equal(t, 0, n)

	// The same through ALTER TABLE.
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN touched_at timestamptz DEFAULT now()`)
	mustExec(t, conn, `INSERT INTO t (label) VALUES ('y')`)
	var touched string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT touched_at FROM t WHERE label = 'y'`).Scan(&touched))
	assert.NotEmpty(t, touched)
}

// A computed default through ADD COLUMN: SQLite refuses it outright, so the
// table is rebuilt with the column in its CREATE TABLE. Existing rows keep
// their data and take the new column's default.
func TestAddColumnComputedDefaultKeepsData(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key, label text)`)
	mustExec(t, conn, `CREATE INDEX idx_label ON t (label)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 'first'), (2, 'second')`)

	mustExec(t, conn, `ALTER TABLE t ADD COLUMN created_at timestamptz DEFAULT now()`)

	// The existing rows are still there, with their data.
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)

	var label string
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM t WHERE id = 2`).Scan(&label))
	assert.Equal(t, "second", label)

	// New rows get the default.
	mustExec(t, conn, `INSERT INTO t (id, label) VALUES (3, 'third')`)
	var createdAt string
	require.NoError(t, conn.QueryRow(ctx, `SELECT created_at FROM t WHERE id = 3`).Scan(&createdAt))
	assert.NotEmpty(t, createdAt)

	// The index survived the rebuild.
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_indexes WHERE tablename = 't' AND indexname = 'idx_label'`).Scan(&n))
	assert.Equal(t, 1, n, "the index was lost in the rebuild")

	// A constant default still takes the plain path.
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN n int DEFAULT 0`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT n FROM t WHERE id = 1`).Scan(&n))
	assert.Equal(t, 0, n)
}

func TestAddColumnComputedDefaultExtendedProtocol(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (id int primary key)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1)`)
	mustExec(t, conn, `ALTER TABLE t ADD COLUMN created_at timestamptz DEFAULT now()`)

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 1, n)
}

// The same through a schema-qualified table, which is what an ORM emits once
// the application uses schemas at all.
func TestAddColumnComputedDefaultOnQualifiedTable(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, label text)`)
	mustExec(t, conn, `INSERT INTO "sales"."orders" VALUES (1, 'first')`)

	mustExec(t, conn, `ALTER TABLE "sales"."orders" ADD COLUMN created_at timestamptz DEFAULT now()`)

	var label string
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM "sales"."orders" WHERE id = 1`).Scan(&label))
	assert.Equal(t, "first", label, "the rebuild lost the existing row")

	mustExec(t, conn, `INSERT INTO "sales"."orders" (id, label) VALUES (2, 'second')`)
	var createdAt string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT created_at FROM "sales"."orders" WHERE id = 2`).Scan(&createdAt))
	assert.NotEmpty(t, createdAt)

	// The table stayed in its schema.
	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'orders'`).Scan(&schema))
	assert.Equal(t, "sales", schema)
}

func TestAddColumnComputedDefaultQualifiedMultiFileMode(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, label text)`)
	mustExec(t, conn, `INSERT INTO "sales"."orders" VALUES (1, 'first')`)

	mustExec(t, conn, `ALTER TABLE "sales"."orders" ADD COLUMN created_at timestamptz DEFAULT now()`)

	var label string
	require.NoError(t, conn.QueryRow(ctx, `SELECT label FROM "sales"."orders" WHERE id = 1`).Scan(&label))
	assert.Equal(t, "first", label)

	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'orders'`).Scan(&schema))
	assert.Equal(t, "sales", schema, "the table left its schema in the rebuild")
}

// Rebuilding a table that another one references: SQLite resolves those
// foreign keys while the table is briefly gone, so the rebuild has to defer
// enforcement to the end of its transaction.
func TestAddColumnComputedDefaultOnReferencedTable(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE customers (id int primary key, name text)`)
	mustExec(t, conn, `CREATE TABLE orders (id int primary key, customer_id int REFERENCES customers(id))`)
	mustExec(t, conn, `INSERT INTO customers VALUES (1, 'ACME')`)
	mustExec(t, conn, `INSERT INTO orders VALUES (10, 1)`)

	mustExec(t, conn, `ALTER TABLE customers ADD COLUMN created_at timestamptz DEFAULT now()`)

	var name string
	require.NoError(t, conn.QueryRow(ctx, `SELECT name FROM customers WHERE id = 1`).Scan(&name))
	assert.Equal(t, "ACME", name)

	// The foreign key still holds afterwards.
	_, err := conn.Exec(ctx, `INSERT INTO orders VALUES (11, 999)`)
	require.Error(t, err, "the foreign key stopped being enforced after the rebuild")
}

// The same rebuild inside a schema, with an index on the table and another
// table referencing it: the replayed index has to go back into the schema, not
// into public, and the foreign key has to survive.
func TestAddColumnComputedDefaultRebuildsSchemaAuxObjects(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA acme`)
	mustExec(t, conn, `CREATE TABLE "acme"."users" (id text primary key, email text)`)
	mustExec(t, conn, `CREATE INDEX "idx_users_email" ON "acme"."users" ("email")`)
	mustExec(t, conn, `CREATE TABLE "acme"."posts" (
		id text primary key, user_id text REFERENCES "acme"."users"("id"))`)
	mustExec(t, conn, `INSERT INTO "acme"."users" VALUES ('u1', 'a@a')`)
	mustExec(t, conn, `INSERT INTO "acme"."posts" VALUES ('p1', 'u1')`)

	mustExec(t, conn, `ALTER TABLE "acme"."users" ADD COLUMN created_at timestamptz DEFAULT now()`)

	// The data survived.
	var email string
	require.NoError(t, conn.QueryRow(ctx, `SELECT email FROM "acme"."users" WHERE id = 'u1'`).Scan(&email))
	assert.Equal(t, "a@a", email)

	// The index went back into the schema, not into public.
	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT schemaname FROM pg_indexes WHERE indexname = 'idx_users_email'`).Scan(&schema))
	assert.Equal(t, "acme", schema)

	// And the foreign key is still enforced.
	_, err := conn.Exec(ctx, `INSERT INTO "acme"."posts" VALUES ('p2', 'nobody')`)
	require.Error(t, err, "the foreign key stopped being enforced after the rebuild")
}

// ALTER TABLE on a schema-qualified table, in the spelling an ORM emits.
func TestAlterTableOnQualifiedTable(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id int primary key, total int)`)
	mustExec(t, conn, `INSERT INTO "sales"."orders" VALUES (1, 10)`)

	mustExec(t, conn, `ALTER TABLE "sales"."orders" ALTER COLUMN total TYPE bigint`)
	mustExec(t, conn, `ALTER TABLE "sales"."orders" ADD COLUMN label text`)

	var total int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT total FROM "sales"."orders" WHERE id = 1`).Scan(&total))
	assert.Equal(t, int64(10), total)

	var schema string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT table_schema FROM information_schema.tables WHERE table_name = 'orders'`).Scan(&schema))
	assert.Equal(t, "sales", schema)
}
