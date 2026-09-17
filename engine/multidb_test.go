package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A process serves one engine per database file. Anything the catalog or the
// statement rewrites know about "the database" therefore has to be per engine:
// while it was process-global, the second engine to open overwrote the first's
// name and schema list, and every connection got whichever won.
func TestTwoDatabasesInOneProcess(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	shop, err := Open(filepath.Join(dir, "shop.db"))
	require.NoError(t, err)
	t.Cleanup(func() { shop.Close() })

	blog, err := Open(filepath.Join(dir, "blog.db"))
	require.NoError(t, err)
	t.Cleanup(func() { blog.Close() })

	// Each answers with its own name, whichever opened last.
	name := func(e *SQLite) string {
		rs, err := e.Execute(ctx, `SELECT current_database()`, nil)
		require.NoError(t, err)
		return asString(rs.Rows[0][0])
	}
	assert.Equal(t, "shop", name(shop))
	assert.Equal(t, "blog", name(blog))

	// Schemas are per database: one's list must not leak into the other's
	// name resolution.
	require.NoError(t, shop.CreateSchema(ctx, "vendas", false))
	mustExec(t, shop, `CREATE TABLE vendas.pedidos (id INTEGER PRIMARY KEY)`)

	schemas := func(e *SQLite) []string {
		rs, err := e.Execute(ctx, `SELECT nspname FROM pg_namespace
			WHERE nspname NOT IN ('pg_catalog','information_schema') ORDER BY nspname`, nil)
		require.NoError(t, err)
		var out []string
		for _, r := range rs.Rows {
			out = append(out, asString(r[0]))
		}
		return out
	}
	assert.Equal(t, []string{"public", "vendas"}, schemas(shop))
	assert.Equal(t, []string{"public"}, schemas(blog), "the other database's schema leaked")

	// blog has no "vendas", so the name must not resolve there.
	_, err = blog.Execute(ctx, `SELECT * FROM vendas.pedidos`, nil)
	assert.Error(t, err, "a table from another database was reachable")

	// And the tables stay apart.
	mustExec(t, shop, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`)
	mustExec(t, shop, `INSERT INTO t (v) VALUES ('shop-row')`)
	mustExec(t, blog, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`)
	mustExec(t, blog, `INSERT INTO t (v) VALUES ('blog-row')`)

	for _, tc := range []struct {
		eng  *SQLite
		want string
	}{{shop, "shop-row"}, {blog, "blog-row"}} {
		rs, err := tc.eng.Execute(ctx, `SELECT v FROM t`, nil)
		require.NoError(t, err)
		require.Len(t, rs.Rows, 1)
		assert.Equal(t, tc.want, asString(rs.Rows[0][0]))
	}

	// The catalog names each database as its own.
	catalog := func(e *SQLite) string {
		// At this level the view carries its quoted dotted name; the protocol
		// is what lets a client write information_schema.tables.
		rs, err := e.Execute(ctx,
			`SELECT table_catalog FROM "information_schema.tables" WHERE table_name = 't'`, nil)
		require.NoError(t, err)
		return asString(rs.Rows[0][0])
	}
	assert.Equal(t, "shop", catalog(shop))
	assert.Equal(t, "blog", catalog(blog))
}
