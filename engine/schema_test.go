package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSchemaLifecycle(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "system.db")
	vendasFile := filepath.Join(dir, "system.vendas.db")
	ctx := context.Background()

	eng, err := Open(main)
	require.NoError(t, err)
	t.Cleanup(func() { eng.Close() })

	// CREATE SCHEMA creates and attaches a sibling file.
	require.NoError(t, eng.CreateSchema(ctx, "vendas", false))
	assert.FileExists(t, vendasFile)

	// A table created in the schema lives in that file and is queryable.
	mustExec(t, eng, `CREATE TABLE vendas.pedidos (id INTEGER PRIMARY KEY, total REAL)`)
	mustExec(t, eng, `INSERT INTO vendas.pedidos (total) VALUES (9.9)`)
	sel, err := eng.Execute(ctx, `SELECT total FROM vendas.pedidos`, nil)
	require.NoError(t, err)
	require.Len(t, sel.Rows, 1)

	// The catalog reflects the schema and its table under the right namespace.
	ns, err := eng.Execute(ctx, `SELECT nspname FROM pg_namespace WHERE nspname = 'vendas'`, nil)
	require.NoError(t, err)
	assert.Len(t, ns.Rows, 1)

	rel, err := eng.Execute(ctx, `
		SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'vendas' AND c.relkind = 'r'`, nil)
	require.NoError(t, err)
	require.Len(t, rel.Rows, 1)
	assert.Equal(t, "pedidos", asString(rel.Rows[0][0]))

	// public and vendas tables don't collide in the catalog.
	mustExec(t, eng, `CREATE TABLE clientes (id INTEGER PRIMARY KEY)`)
	all, err := eng.Execute(ctx, `SELECT count(*) FROM pg_class WHERE relkind = 'r'`, nil)
	require.NoError(t, err)
	assert.Equal(t, int64(2), all.Rows[0][0]) // clientes (public) + pedidos (vendas)

	// DROP SCHEMA without cascade refuses a non-empty schema.
	require.Error(t, eng.DropSchema(ctx, "vendas", false, false))
	require.NoError(t, eng.DropSchema(ctx, "vendas", false, true))
	assert.NoFileExists(t, vendasFile)
}

func TestSchemaPersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "system.db")
	ctx := context.Background()

	eng, err := Open(main)
	require.NoError(t, err)
	require.NoError(t, eng.CreateSchema(ctx, "auth", false))
	mustExec(t, eng, `CREATE TABLE auth.users (id INTEGER PRIMARY KEY, email TEXT)`)
	eng.Close()

	// Reopening rediscovers and reattaches the schema file.
	eng2, err := Open(main)
	require.NoError(t, err)
	t.Cleanup(func() { eng2.Close() })

	sel, err := eng2.Execute(ctx, `SELECT email FROM auth.users`, nil)
	require.NoError(t, err)
	assert.NotNil(t, sel)

	ns, err := eng2.Execute(ctx, `SELECT nspname FROM pg_namespace WHERE nspname = 'auth'`, nil)
	require.NoError(t, err)
	assert.Len(t, ns.Rows, 1)
}

func TestSchemaDiscoveryMultiple(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "hello.db")
	ctx := context.Background()

	// First run: create several schemas, each with a table.
	eng, err := Open(main)
	require.NoError(t, err)
	for _, s := range []string{"bla", "ble", "vendas"} {
		require.NoError(t, eng.CreateSchema(ctx, s, false))
		mustExec(t, eng, "CREATE TABLE "+s+".t (id INTEGER PRIMARY KEY, v TEXT)")
		mustExec(t, eng, "INSERT INTO "+s+".t (v) VALUES ('"+s+"-row')")
	}
	eng.Close()

	// The sibling files exist on disk.
	for _, s := range []string{"bla", "ble", "vendas"} {
		assert.FileExists(t, filepath.Join(dir, "hello."+s+".db"))
	}

	// Second run: a fresh engine pointed only at hello.db must auto-discover
	// and attach ALL sibling schema files.
	eng2, err := Open(main)
	require.NoError(t, err)
	t.Cleanup(func() { eng2.Close() })

	// Every schema shows up in the catalog.
	ns, err := eng2.Execute(ctx, `
		SELECT nspname FROM pg_namespace
		WHERE nspname NOT IN ('pg_catalog','information_schema')
		ORDER BY nspname`, nil)
	require.NoError(t, err)
	var got []string
	for _, row := range ns.Rows {
		got = append(got, asString(row[0]))
	}
	assert.Equal(t, []string{"bla", "ble", "public", "vendas"}, got)

	// Each schema's data is queryable and the tables don't collide.
	for _, s := range []string{"bla", "ble", "vendas"} {
		sel, err := eng2.Execute(ctx, "SELECT v FROM "+s+".t", nil)
		require.NoErrorf(t, err, "query %s.t", s)
		require.Len(t, sel.Rows, 1)
		assert.Equal(t, s+"-row", asString(sel.Rows[0][0]))
	}

	// pg_class exposes all three tables under their own namespaces.
	rels, err := eng2.Execute(ctx, `
		SELECT n.nspname, c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname <> 'public'
		ORDER BY n.nspname`, nil)
	require.NoError(t, err)
	assert.Len(t, rels.Rows, 3)
}

func TestSchemaNameValidation(t *testing.T) {
	assert.True(t, validSchemaName("vendas"))
	assert.True(t, validSchemaName("auth_2"))
	assert.False(t, validSchemaName("public")) // reserved
	assert.False(t, validSchemaName("main"))   // reserved
	assert.False(t, validSchemaName("with space"))
	assert.False(t, validSchemaName("1abc"))
	assert.False(t, validSchemaName("a.b"))
}
