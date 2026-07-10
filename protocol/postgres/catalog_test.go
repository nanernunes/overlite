package postgres_test

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogVersion(t *testing.T) {
	conn := connect(t, startServer(t))

	var v string
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT version()").Scan(&v))
	assert.Contains(t, v, "overlite")
	assert.Contains(t, v, "PostgreSQL")
}

func TestCatalogCurrentSchema(t *testing.T) {
	conn := connect(t, startServer(t))

	var schema string
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT current_schema()").Scan(&schema))
	assert.Equal(t, "public", schema)
}

func TestCatalogInformationSchemaTables(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)`)
	mustExec(t, conn, `CREATE TABLE orders (id INTEGER PRIMARY KEY, total REAL)`)

	rows, err := conn.Query(ctx,
		`SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' ORDER BY table_name`)
	require.NoError(t, err)

	var names []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		names = append(names, n)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []string{"orders", "users"}, names)
}

func TestCatalogInformationSchemaColumns(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)

	rows, err := conn.Query(ctx,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'users'`)
	require.NoError(t, err)

	var cols []string
	for rows.Next() {
		var c string
		require.NoError(t, rows.Scan(&c))
		cols = append(cols, c)
	}
	require.NoError(t, rows.Err())
	sort.Strings(cols)
	assert.Equal(t, []string{"age", "id", "name"}, cols)
}
