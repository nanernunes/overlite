package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 'true'::boolean was CAST('true' AS boolean): NUMERIC affinity, so any text
// became 0 and a true cast to false without an error. (A cast's result is
// still described by its value, SQLite's 1 or 0, not as bool.)
func TestBooleanCast(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	for in, want := range map[string]int{
		`'true'::boolean`: 1, `'t'::bool`: 1, `'yes'::boolean`: 1, `'on'::boolean`: 1,
		`'1'::boolean`: 1, `1::boolean`: 1, `'false'::boolean`: 0, `'f'::bool`: 0,
		`'off'::boolean`: 0, `0::boolean`: 0, `CAST('true' AS boolean)`: 1,
		`CAST(' Yes ' AS pg_catalog.bool)`: 1,
	} {
		var got int
		require.NoError(t, conn.QueryRow(ctx, `SELECT `+in).Scan(&got), in)
		assert.Equal(t, want, got, in)
	}

	var null *int
	require.NoError(t, conn.QueryRow(ctx, `SELECT NULL::boolean`).Scan(&null))
	assert.Nil(t, null)

	_, err := conn.Exec(ctx, `SELECT 'maybe'::boolean`)
	require.Error(t, err, "Postgres rejects what is not a boolean")
	assert.Contains(t, err.Error(), `invalid input syntax for type boolean: "maybe"`)

	// A bound parameter cast to boolean, as `$1::boolean` with "true".
	mustExec(t, conn, `CREATE TABLE b (k int, v boolean)`)
	res := conn.PgConn().ExecParams(ctx, `INSERT INTO b VALUES (1, $1::boolean)`,
		[][]byte{[]byte("true")}, nil, nil, nil).Read()
	require.NoError(t, res.Err)
	var v bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT v FROM b WHERE k = 1`).Scan(&v))
	assert.True(t, v)
}
