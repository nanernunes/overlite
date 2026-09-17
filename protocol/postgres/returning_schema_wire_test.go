package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RETURNING against a schema-qualified table, through the extended protocol
// (which is where the statement gets described, and where the table name was
// mis-parsed). Every ORM that quotes identifiers and reads a generated key back
// lands here.

func TestReturningOnQualifiedTable(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA sales`)
	mustExec(t, conn, `CREATE TABLE "sales"."orders" (id serial primary key, total int)`)

	var id int
	require.NoError(t, conn.QueryRow(ctx,
		`INSERT INTO "sales"."orders" (total) VALUES ($1) RETURNING id`, 10).Scan(&id))
	assert.Positive(t, id)

	var total int
	require.NoError(t, conn.QueryRow(ctx,
		`UPDATE "sales"."orders" SET total = total + $1 WHERE id = $2 RETURNING total`, 5, id).Scan(&total))
	assert.Equal(t, 15, total)

	var deleted int
	require.NoError(t, conn.QueryRow(ctx,
		`DELETE FROM "sales"."orders" WHERE id = $1 RETURNING id`, id).Scan(&deleted))
	assert.Equal(t, id, deleted)
}
