package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNumericExact: numeric columns store exact decimals (no float loss),
// compare/order numerically, and sum/avg exactly.
func TestNumericExact(t *testing.T) {
	conn := connect(t, startServer(t)) // simple protocol (text), like psql
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, amount numeric(20,2))`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1,'0.10'),(2,'0.20'),(3,'123456789012345.67'),(4,'9'),(5,'10')`)
	require.NoError(t, err)

	// Exact round-trip — a value beyond float64 precision survives.
	var big string
	require.NoError(t, conn.QueryRow(ctx, `SELECT amount FROM t WHERE id = 3`).Scan(&big))
	assert.Equal(t, "123456789012345.67", big)

	// Numeric ordering (not textual: 9 < 10, 0.10 < 9).
	rows, err := conn.Query(ctx, `SELECT id FROM t ORDER BY amount`)
	require.NoError(t, err)
	var order []int
	for rows.Next() {
		var id int
		require.NoError(t, rows.Scan(&id))
		order = append(order, id)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []int{1, 2, 4, 5, 3}, order)

	// Numeric comparison.
	var cnt int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t WHERE amount > '5'`).Scan(&cnt))
	assert.Equal(t, 3, cnt) // 9, 10, and the big one

	// Exact sum (0.10 + 0.20 == 0.30, not 0.30000000000000004).
	var sum string
	require.NoError(t, conn.QueryRow(ctx, `SELECT sum(amount) FROM t WHERE id IN (1,2)`).Scan(&sum))
	assert.Equal(t, "0.3", sum)

	// Exact avg.
	var avg string
	require.NoError(t, conn.QueryRow(ctx, `SELECT avg(amount) FROM t WHERE id IN (4,5)`).Scan(&avg))
	assert.Equal(t, "9.5", avg)
}
