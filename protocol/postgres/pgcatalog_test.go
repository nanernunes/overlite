package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The queries here mirror the shapes psql (\dt, \d) and GUI clients send:
// joins across pg_class / pg_namespace / pg_attribute / pg_type plus the
// helper functions format_type() and pg_table_is_visible().

func TestPgCatalogListTables(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE orders (id INTEGER PRIMARY KEY)`)

	// The \dt shape: class joined to namespace, filtered to ordinary tables in
	// the public schema and visible in the search path.
	rows, err := conn.Query(ctx, `
		SELECT n.nspname, c.relname, c.relkind
		FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r'
		  AND n.nspname = 'public'
		  AND pg_catalog.pg_table_is_visible(c.oid)
		ORDER BY c.relname`)
	require.NoError(t, err)

	type rel struct {
		schema, name, kind string
	}
	var got []rel
	for rows.Next() {
		var r rel
		require.NoError(t, rows.Scan(&r.schema, &r.name, &r.kind))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []rel{
		{"public", "orders", "r"},
		{"public", "users", "r"},
	}, got)
}

func TestPgCatalogDescribeColumns(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT NOT NULL, bio TEXT)`)

	// The \d <table> shape: attributes joined to class, with human type names
	// via format_type().
	rows, err := conn.Query(ctx, `
		SELECT a.attname,
		       format_type(a.atttypid, a.atttypmod) AS type,
		       a.attnotnull
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'users'
		  AND n.nspname = 'public'
		  AND a.attnum > 0
		  AND NOT a.attisdropped
		ORDER BY a.attnum`)
	require.NoError(t, err)

	type col struct {
		name    string
		typ     string
		notnull bool
	}
	var got []col
	for rows.Next() {
		var c col
		require.NoError(t, rows.Scan(&c.name, &c.typ, &c.notnull))
		got = append(got, c)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []col{
		{"id", "integer", true}, // primary key surfaces as NOT NULL, as in Postgres
		{"name", "text", true},
		{"bio", "text", false},
	}, got)
}

func TestPgCatalogBareNames(t *testing.T) {
	// Clients often reference catalog tables unqualified, relying on
	// pg_catalog being on the search_path.
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (a INTEGER)`)

	var name string
	require.NoError(t, conn.QueryRow(context.Background(),
		`SELECT relname FROM pg_class WHERE relkind = 'r' AND relname NOT LIKE 'pg_%'`).Scan(&name))
	assert.Equal(t, "t", name)
}

func TestPgCatalogTypeJoin(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (a INTEGER, b TEXT, c REAL)`)

	rows, err := conn.Query(ctx, `
		SELECT a.attname, t.typname
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		JOIN pg_catalog.pg_type t ON t.oid = a.atttypid
		WHERE c.relname = 't' AND a.attnum > 0
		ORDER BY a.attnum`)
	require.NoError(t, err)

	got := map[string]string{}
	for rows.Next() {
		var name, typ string
		require.NoError(t, rows.Scan(&name, &typ))
		got[name] = typ
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[string]string{"a": "int4", "b": "text", "c": "float8"}, got)
}

func TestPgCatalogPartitionFunctions(t *testing.T) {
	// psql's \d calls partition helpers; they must at least resolve.
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (a INTEGER)`)
	for _, fn := range []string{
		"pg_catalog.pg_partition_root(c.oid)",
		"pg_catalog.pg_partition_ancestors(c.oid)",
		"pg_catalog.pg_get_partkeydef(c.oid)",
	} {
		_, err := conn.Exec(context.Background(),
			fmt.Sprintf("SELECT %s FROM pg_catalog.pg_class c LIMIT 1", fn))
		require.NoErrorf(t, err, "function %s must resolve", fn)
	}
}

func TestPgCatalogConstraintAndIndexColumns(t *testing.T) {
	// The column info that drives \d's Indexes / Foreign-key sections.
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)
	mustExec(t, conn, `CREATE TABLE pedidos (id INTEGER PRIMARY KEY, cliente_id INTEGER REFERENCES clientes(id))`)
	mustExec(t, conn, `CREATE INDEX idx_cli ON pedidos(cliente_id)`)

	var pkCols string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT ov_cols FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
		WHERE c.relname = 'clientes' AND con.contype = 'p'`).Scan(&pkCols))
	assert.Equal(t, "id", pkCols)

	var idxCols string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT i.ov_cols FROM pg_catalog.pg_index i
		JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid
		WHERE c.relname = 'idx_cli'`).Scan(&idxCols))
	assert.Equal(t, "cliente_id", idxCols)

	var fkCols, fkRef string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT ov_cols, ov_ref FROM pg_catalog.pg_constraint con
		JOIN pg_catalog.pg_class c ON c.oid = con.conrelid
		WHERE c.relname = 'pedidos' AND con.contype = 'f'`).Scan(&fkCols, &fkRef))
	assert.Equal(t, "cliente_id", fkCols)
	assert.Equal(t, "clientes(id)", fkRef)
}

func TestPgCatalogRegexpOperator(t *testing.T) {
	// psql's \dt filters schemas with "!~ '^pg_toast'"; the ~ / !~ operators
	// must map onto SQLite's REGEXP.
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE real_table (a INTEGER)`)

	rows, err := conn.Query(context.Background(), `
		SELECT nspname FROM pg_catalog.pg_namespace
		WHERE nspname !~ '^pg_' AND nspname ~ 'pub'`)
	require.NoError(t, err)
	var got []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		got = append(got, s)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"public"}, got)
}
