package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSearchPath: SET search_path makes unqualified names resolve per the path
// (single-file mode), SHOW reflects it, and CREATE goes to the first schema.
func TestSearchPath(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA app")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "CREATE TABLE app.widget (id int, name text)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO app.widget VALUES (1, 'gizmo')")
	require.NoError(t, err)

	// Without a path, an unqualified name resolves in public only → not found.
	_, err = conn.Exec(ctx, "SELECT * FROM widget")
	require.Error(t, err)

	// With the path, the unqualified name resolves in app.
	_, err = conn.Exec(ctx, "SET search_path TO app, public")
	require.NoError(t, err)
	var name string
	require.NoError(t, conn.QueryRow(ctx, "SELECT name FROM widget WHERE id = 1").Scan(&name))
	assert.Equal(t, "gizmo", name)

	// SHOW reflects the path.
	var sp string
	require.NoError(t, conn.QueryRow(ctx, "SHOW search_path").Scan(&sp))
	assert.Contains(t, sp, "app")

	// CREATE TABLE (unqualified) lands in the first path schema.
	_, err = conn.Exec(ctx, "CREATE TABLE gadget (id int)")
	require.NoError(t, err)
	var ns string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT n.nspname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'gadget'`).Scan(&ns))
	assert.Equal(t, "app", ns)

	// A name only in public still resolves (not shadowed in app).
	_, err = conn.Exec(ctx, "CREATE TABLE public.pubonly (id int)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO pubonly VALUES (5)")
	require.NoError(t, err)
	var id int
	require.NoError(t, conn.QueryRow(ctx, "SELECT id FROM pubonly").Scan(&id))
	assert.Equal(t, 5, id)

	// RESET restores the default (public-only resolution).
	_, err = conn.Exec(ctx, "RESET search_path")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "SELECT * FROM widget")
	require.Error(t, err)
}
