package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommentOn: COMMENT ON stores comments, visible via pg_description /
// obj_description / col_description (what psql's \d+ reads).
func TestCommentOn(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, name text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `COMMENT ON TABLE t IS 'a table'`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `COMMENT ON COLUMN t.name IS 'the name'`)
	require.NoError(t, err)

	var tc, cc string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT obj_description(c.oid, 'pg_class') FROM pg_class c WHERE relname='t'`).Scan(&tc))
	assert.Equal(t, "a table", tc)
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT col_description(a.attrelid, a.attnum) FROM pg_attribute a
		 JOIN pg_class c ON c.oid=a.attrelid WHERE c.relname='t' AND a.attname='name'`).Scan(&cc))
	assert.Equal(t, "the name", cc)

	// pg_description carries a row per comment.
	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_description d JOIN pg_class c ON c.oid=d.objoid WHERE c.relname='t'`).Scan(&n))
	assert.Equal(t, 2, n)

	// COMMENT ... IS NULL removes it.
	_, err = conn.Exec(ctx, `COMMENT ON TABLE t IS NULL`)
	require.NoError(t, err)
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM pg_description d JOIN pg_class c ON c.oid=d.objoid WHERE c.relname='t'`).Scan(&n))
	assert.Equal(t, 1, n) // only the column comment remains
}
