package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CREATE SCHEMA over the wire, then use and introspect a schema-qualified table
// (the schema is a separate attached SQLite file behind the scenes).
func TestSchemaOverWire(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "CREATE SCHEMA vendas")
	require.NoError(t, err)

	_, err = conn.Exec(ctx, "CREATE TABLE vendas.pedidos (id INTEGER PRIMARY KEY, total REAL)")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "INSERT INTO vendas.pedidos (total) VALUES (5.5)")
	require.NoError(t, err)

	var total float64
	require.NoError(t, conn.QueryRow(ctx, "SELECT total FROM vendas.pedidos").Scan(&total))
	assert.Equal(t, 5.5, total)

	// A public table stays independent.
	_, err = conn.Exec(ctx, "CREATE TABLE clientes (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)

	// \dn lists both schemas.
	schemas := queryColumn(t, conn, psqlListSchemas, 0)
	assert.Contains(t, schemas, "public")
	assert.Contains(t, schemas, "vendas")

	// The table is visible in its schema via pg_class/pg_namespace.
	var rel string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT c.relname FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'vendas' AND c.relkind = 'r'`).Scan(&rel))
	assert.Equal(t, "pedidos", rel)

	// CREATE SCHEMA IF NOT EXISTS is idempotent; DROP SCHEMA CASCADE works.
	_, err = conn.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS vendas")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "DROP SCHEMA vendas CASCADE")
	require.NoError(t, err)

	schemas = queryColumn(t, conn, psqlListSchemas, 0)
	assert.NotContains(t, schemas, "vendas")
}
