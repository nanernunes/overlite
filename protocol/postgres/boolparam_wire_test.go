package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A boolean bound as a parameter is one. Parameters were described with no
// type, so pgx sent a Go bool as the text "t", stored as that string; read
// back in binary it became false — a flag turned on, through GORM, stayed
// off. Parameters set into a column now take its type, and a stored "t"
// reads as true either way.
func TestBooleanParameters(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE flags (id text primary key, on_ boolean NOT NULL DEFAULT false, name text)`)
	_, err := conn.Exec(ctx, `INSERT INTO "flags" ("id","on_","name") VALUES ($1,$2,$3),($4,$5,$6)`, "a", true, "x", "b", false, "t")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `UPDATE "flags" SET "on_"=$1,"name"=$2 WHERE "id" = $3`, true, "y", "b")
	require.NoError(t, err)

	var a, b bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT on_ FROM flags WHERE id = 'a'`).Scan(&a))
	require.NoError(t, conn.QueryRow(ctx, `SELECT on_ FROM flags WHERE id = 'b'`).Scan(&b))
	assert.True(t, a, "inserted as true")
	assert.True(t, b, "updated to true")

	// Stored as SQLite's 1, so a literal finds them; a text column keeps its "t".
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM flags WHERE on_ = true`).Scan(&n))
	assert.Equal(t, 2, n)
	var name string
	require.NoError(t, conn.QueryRow(ctx, `SELECT name FROM flags WHERE id = 'a'`).Scan(&name))
	assert.Equal(t, "x", name)

	// What older versions stored, "t" as text, reads as true in binary too.
	mustExec(t, conn, `INSERT INTO flags (id, on_) VALUES ('c', 't')`)
	var c bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT on_ FROM flags WHERE id = 'c'`).Scan(&c))
	assert.True(t, c, "a stored 't'")
}
