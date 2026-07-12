package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSchemaInTransaction: in the default single-file mode a schema is an
// ordinary write, so CREATE/DROP SCHEMA work inside a transaction (impossible
// in multi-file mode, where ATTACH can't run in a tx) and roll back with it.
func TestSchemaInTransaction(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// CREATE SCHEMA + a table + data, all in one transaction.
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "CREATE SCHEMA sales")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "CREATE TABLE sales.orders (id int primary key, total numeric)")
	require.NoError(t, err)
	_, err = tx.Exec(ctx, "INSERT INTO sales.orders VALUES (1, 12.34)")
	require.NoError(t, err)
	require.NoError(t, tx.Commit(ctx))

	var total string
	require.NoError(t, conn.QueryRow(ctx, "SELECT total FROM sales.orders").Scan(&total))
	assert.Equal(t, "12.34", total)

	// A rolled-back CREATE SCHEMA leaves nothing behind.
	tx2, err := conn.Begin(ctx)
	require.NoError(t, err)
	_, err = tx2.Exec(ctx, "CREATE SCHEMA temp_stuff")
	require.NoError(t, err)
	require.NoError(t, tx2.Rollback(ctx))

	var n int
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname = 'temp_stuff'").Scan(&n))
	assert.Equal(t, 0, n)
}

// TestCrossSchemaForeignKey: a foreign key from a public table to a table in
// another schema is enforced (both live in the one file, unlike multi-file mode
// where SQLite can't enforce FKs across attached databases).
func TestCrossSchemaForeignKey(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA inv")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE inv.product (id int primary key)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO inv.product VALUES (1)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx,
		"CREATE TABLE public.line (id int primary key, pid int references inv.product(id))")
	require.NoError(t, err)

	// A dangling reference is rejected; a valid one is accepted.
	_, err = conn.Exec(ctx, "INSERT INTO public.line VALUES (1, 999)")
	require.Error(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO public.line VALUES (1, 1)")
	require.NoError(t, err)
}

// TestDropSchemaCascadeInTx: DROP SCHEMA CASCADE removes the schema and its
// prefixed tables, transactionally.
func TestDropSchemaCascadeInTx(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA s1")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE s1.t (id int)")
	require.NoError(t, err)

	// Non-empty schema needs CASCADE.
	_, err = conn.Exec(ctx, "DROP SCHEMA s1")
	require.Error(t, err)

	_, err = conn.Exec(ctx, "DROP SCHEMA s1 CASCADE")
	require.NoError(t, err)
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname = 's1'").Scan(&n))
	assert.Equal(t, 0, n)
	// The table is gone too.
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_class WHERE relname = 't'").Scan(&n))
	assert.Equal(t, 0, n)
}
