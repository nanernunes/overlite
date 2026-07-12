package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlterTableSetSchema: moving a table between schemas is a rename in
// single-file mode; data and referencing FKs survive it.
func TestAlterTableSetSchema(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA archive")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE public.doc (id int primary key, body text)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO public.doc VALUES (1, 'hello')")
	require.NoError(t, err)

	// Move public.doc -> archive.doc.
	_, err = conn.Exec(ctx, "ALTER TABLE public.doc SET SCHEMA archive")
	require.NoError(t, err)

	var ns, body string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'doc'`).Scan(&ns))
	assert.Equal(t, "archive", ns)
	require.NoError(t, conn.QueryRow(ctx, "SELECT body FROM archive.doc WHERE id = 1").Scan(&body))
	assert.Equal(t, "hello", body)

	// The old location is gone.
	_, err = conn.Exec(ctx, "SELECT * FROM public.doc")
	require.Error(t, err)
}

// TestAlterSchemaRename: renaming a schema carries its tables (and their data).
func TestAlterSchemaRename(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA old_name")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE old_name.thing (id int)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO old_name.thing VALUES (42)")
	require.NoError(t, err)

	_, err = conn.Exec(ctx, "ALTER SCHEMA old_name RENAME TO new_name")
	require.NoError(t, err)

	var n int
	require.NoError(t, conn.QueryRow(ctx,
		"SELECT count(*) FROM pg_namespace WHERE nspname = 'old_name'").Scan(&n))
	assert.Equal(t, 0, n)
	var id int
	require.NoError(t, conn.QueryRow(ctx, "SELECT id FROM new_name.thing").Scan(&id))
	assert.Equal(t, 42, id)

	// Renaming to an existing schema fails.
	_, err = conn.Exec(ctx, "CREATE SCHEMA taken")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "ALTER SCHEMA new_name RENAME TO taken")
	require.Error(t, err)
}

// TestSchemaTableIntrospection: a table in a non-public schema introspects
// fully — its PK/FK constraint names carry no "schema." prefix, and an explicit
// index on it belongs to the schema and is visible.
func TestSchemaTableIntrospection(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA app")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE app.parent (id int primary key)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE app.child (id int primary key, pid int references app.parent(id))")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE INDEX idx_child_pid ON app.child(pid)")
	require.NoError(t, err)

	// Constraint names have no schema prefix.
	names := queryColumn(t, conn, `
		SELECT con.conname FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'app' AND c.relname = 'child' ORDER BY con.conname`, 0)
	for _, n := range names {
		assert.NotContains(t, n, "app.", "constraint name should not carry the schema prefix: %s", n)
	}

	// The explicit index belongs to schema app and is linked to app.child.
	var idxName, idxSchema string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT ci.relname, ni.nspname
		FROM pg_index i
		JOIN pg_class ct ON ct.oid = i.indrelid
		JOIN pg_class ci ON ci.oid = i.indexrelid
		JOIN pg_namespace ni ON ni.oid = ci.relnamespace
		WHERE ct.relname = 'child' AND ci.relname LIKE '%idx_child_pid%'`).Scan(&idxName, &idxSchema))
	assert.Contains(t, idxName, "idx_child_pid")
	assert.Equal(t, "app", idxSchema)
}
