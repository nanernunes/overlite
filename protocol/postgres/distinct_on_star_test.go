package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDistinctOnStar: SELECT DISTINCT ON (col) * over a single table keeps the
// first row per group per the ORDER BY (SQLite has no DISTINCT ON, and a "*"
// select list previously errored outright).
func TestDistinctOnStar(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE events (user_id int, ts int, action text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO events VALUES
		(1, 10, 'a'), (1, 30, 'c'), (1, 20, 'b'),
		(2, 5, 'x'), (2, 8, 'y')`)
	require.NoError(t, err)

	// Latest action per user (highest ts).
	rows, err := conn.Query(ctx,
		`SELECT DISTINCT ON (user_id) * FROM events ORDER BY user_id, ts DESC`)
	require.NoError(t, err)
	defer rows.Close()
	got := map[int]string{}
	for rows.Next() {
		var uid, ts int
		var action string
		require.NoError(t, rows.Scan(&uid, &ts, &action))
		got[uid] = action
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, map[int]string{1: "c", 2: "y"}, got)

	// With a WHERE and a table alias.
	var action string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT DISTINCT ON (e.user_id) * FROM events e WHERE e.user_id = 1 ORDER BY e.user_id, e.ts`).
		Scan(new(int), new(int), &action))
	assert.Equal(t, "a", action) // lowest ts for user 1
}
