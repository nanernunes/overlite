package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A cast after an aggregate's FILTER or OVER took only the parenthesised
// clause as its operand: count(*) CAST(FILTER (...) AS int), a syntax error.
func TestCastAfterFilterAndOver(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE s (id int, on_ boolean)`)
	mustExec(t, conn, `INSERT INTO s VALUES (1, true), (2, false), (3, true)`)

	var filtered, windowed, both int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FILTER (WHERE on_)::int FROM s`).Scan(&filtered))
	assert.Equal(t, 2, filtered)

	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) OVER ()::int FROM s LIMIT 1`).Scan(&windowed))
	assert.Equal(t, 3, windowed)

	require.NoError(t, conn.QueryRow(ctx,
		`SELECT sum(id) FILTER (WHERE on_) OVER ()::int FROM s LIMIT 1`).Scan(&both))
	assert.Equal(t, 4, both)
}
