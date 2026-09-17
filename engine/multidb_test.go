package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"overlite/core"

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

// An enum type's oid is its rowid plus a base, and format_type() renders a name
// from a registry shared by the whole process. Two databases numbering from the
// same base therefore produced the same oid for different types, and each
// rendered whichever name was stored last — the other database's.
func TestEnumsDoNotCollideAcrossDatabases(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	open := func(name string) core.Session {
		eng, err := Open(filepath.Join(dir, name+".db"))
		require.NoError(t, err)
		t.Cleanup(func() { eng.Close() })
		// A client session, which is the path that keeps the registry current.
		sess, err := eng.Session(ctx)
		require.NoError(t, err)
		t.Cleanup(func() { sess.Close() })
		return sess
	}

	shop, blog := open("shop"), open("blog")

	exec := func(s core.Session, sql string) {
		_, err := s.Execute(ctx, sql, nil)
		require.NoErrorf(t, err, "exec %s", sql)
	}
	// The first enum in each database, so both are rowid 1.
	exec(shop, `INSERT INTO _overlite_enum_types (typname) VALUES ('order_status')`)
	exec(blog, `INSERT INTO _overlite_enum_types (typname) VALUES ('post_state')`)

	// Their oids differ, which is what keeps the shared registry honest.
	oid := func(s core.Session, typname string) int64 {
		rs, err := s.Execute(ctx, `SELECT oid FROM pg_type WHERE typname = `+sqlQuote(typname), nil)
		require.NoError(t, err)
		require.Len(t, rs.Rows, 1)
		return asInt64(rs.Rows[0][0])
	}
	shopOID, blogOID := oid(shop, "order_status"), oid(blog, "post_state")
	assert.NotEqual(t, shopOID, blogOID, "two databases gave the same oid to different enum types")

	// And each renders its own name, not the other's.
	name := func(s core.Session, o int64) string {
		rs, err := s.Execute(ctx, fmt.Sprintf(`SELECT format_type(%d, NULL)`, o), nil)
		require.NoError(t, err)
		return asString(rs.Rows[0][0])
	}
	assert.Equal(t, "order_status", name(shop, shopOID))
	assert.Equal(t, "post_state", name(blog, blogOID))

	// A connection that arrives later loads the registry on connect and agrees.
	assert.Equal(t, "order_status", name(open("shop"), shopOID))
}
