package postgres_test

import (
	"context"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNoOpUtilityStatements checks statements we accept but don't model run
// without error (so migrations/dumps proceed).
func TestNoOpUtilityStatements(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (id int)`)
	for _, sql := range []string{
		`CREATE EXTENSION IF NOT EXISTS "uuid-ossp"`,
		`CREATE EXTENSION IF NOT EXISTS pgcrypto`,
		`DROP EXTENSION IF EXISTS pgcrypto`,
		`COMMENT ON TABLE t IS 'a table'`,
		`COMMENT ON COLUMN t.id IS 'the id'`,
		`NOTIFY some_channel`,
	} {
		mustExec(t, conn, sql)
	}
	// The connection still works afterwards.
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestUUIDTypeAndGenerators(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// gen_random_uuid()/uuid_generate_v4() produce distinct valid v4 UUIDs.
	var u1, u2 string
	require.NoError(t, conn.QueryRow(ctx, `SELECT gen_random_uuid()`).Scan(&u1))
	require.NoError(t, conn.QueryRow(ctx, `SELECT uuid_generate_v4()`).Scan(&u2))
	assert.Regexp(t, uuidRE, u1)
	assert.Regexp(t, uuidRE, u2)
	assert.NotEqual(t, u1, u2)

	// A uuid column stores the value as text and reports type uuid in the catalog.
	mustExec(t, conn, `CREATE TABLE items (id uuid, name text)`)
	mustExec(t, conn, `INSERT INTO items (id, name) VALUES (gen_random_uuid(), 'a')`)
	var got string
	require.NoError(t, conn.QueryRow(ctx, `SELECT id FROM items`).Scan(&got))
	assert.Regexp(t, uuidRE, got)

	assert.Equal(t, []string{"uuid"}, queryColumn(t, conn,
		`SELECT format_type(atttypid, NULL) FROM pg_catalog.pg_attribute a
		 JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		 WHERE c.relname = 'items' AND a.attname = 'id'`, 0))

	// A non-deterministic generator yields a fresh value per row.
	rows := queryColumn(t, conn,
		`SELECT gen_random_uuid() FROM (SELECT 1 UNION ALL SELECT 2 UNION ALL SELECT 3)`, 0)
	assert.Len(t, rows, 3)
	assert.NotEqual(t, rows[0], rows[1])
	assert.NotEqual(t, rows[1], rows[2])
}
