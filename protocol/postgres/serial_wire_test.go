package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A SERIAL PRIMARY KEY column must auto-increment like Postgres: inserting rows
// without the id assigns 1, 2, 3, ... and RETURNING gives the generated id.
func TestSerialAutoIncrement(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE users (id SERIAL PRIMARY KEY, nome TEXT NOT NULL)`)
	require.NoError(t, err)

	_, err = conn.Exec(ctx, `INSERT INTO users (nome) VALUES ('ana')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO users (nome) VALUES ('bob')`)
	require.NoError(t, err)

	rows, err := conn.Query(ctx, `SELECT id, nome FROM users ORDER BY id`)
	require.NoError(t, err)
	type u struct {
		id   int
		nome string
	}
	var got []u
	for rows.Next() {
		var x u
		require.NoError(t, rows.Scan(&x.id, &x.nome))
		got = append(got, x)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []u{{1, "ana"}, {2, "bob"}}, got)

	// RETURNING surfaces the generated id.
	var id int
	require.NoError(t, conn.QueryRow(ctx,
		`INSERT INTO users (nome) VALUES ('carol') RETURNING id`).Scan(&id))
	assert.Equal(t, 3, id)

	// The catalog reports the column as integer.
	var typ string
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT format_type(a.atttypid, a.atttypmod)
		FROM pg_catalog.pg_attribute a
		JOIN pg_catalog.pg_class c ON c.oid = a.attrelid
		WHERE c.relname = 'users' AND a.attname = 'id'`).Scan(&typ))
	assert.Equal(t, "integer", typ)
}
