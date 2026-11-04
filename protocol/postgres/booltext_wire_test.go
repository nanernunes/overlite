package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// node-postgres sends every parameter as text with no type, and binds before
// it describes, so a JS true reached a boolean column as the string "true":
// stored as text, `WHERE flag` and `flag = true` never found it again.
func TestBooleanTextParameters(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE flags (id text primary key, on_ boolean NOT NULL DEFAULT false, note text)`)

	// Parse with no types, Bind in text, no Describe of the statement first:
	// what node-postgres does.
	insert := func(id, on, note string) {
		t.Helper()
		res := conn.PgConn().ExecParams(ctx,
			`INSERT INTO flags (id, on_, note) VALUES ($1, $2, $3)
			 ON CONFLICT (id) DO UPDATE SET on_ = excluded.on_, note = excluded.note`,
			[][]byte{[]byte(id), []byte(on), []byte(note)}, nil, nil, nil).Read()
		require.NoError(t, res.Err)
	}
	for id, on := range map[string]string{"a": "true", "b": "t", "c": "yes", "d": " ON ", "e": "1"} {
		insert(id, on, "true")
	}
	for id, off := range map[string]string{"f": "false", "g": "f", "h": "no", "i": "off", "j": "0"} {
		insert(id, off, "false")
	}

	var on, off int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM flags WHERE on_`).Scan(&on))
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM flags WHERE NOT on_`).Scan(&off))
	assert.Equal(t, 5, on, "every Postgres spelling of true")
	assert.Equal(t, 5, off, "every Postgres spelling of false")

	var stored string
	require.NoError(t, conn.QueryRow(ctx, `SELECT typeof(on_) FROM flags WHERE id = 'a'`).Scan(&stored))
	assert.Equal(t, "integer", stored, "stored as SQLite's 1, not the text")

	// A text column keeps what it was given.
	var note string
	require.NoError(t, conn.QueryRow(ctx, `SELECT note FROM flags WHERE id = 'a'`).Scan(&note))
	assert.Equal(t, "true", note)

	// With no column list the values fill the columns in order.
	res0 := conn.PgConn().ExecParams(ctx, `INSERT INTO flags VALUES ($1, $2, $3)`,
		[][]byte{[]byte("k"), []byte("true"), []byte("true")}, nil, nil, nil).Read()
	require.NoError(t, res0.Err)
	var k bool
	var kNote string
	require.NoError(t, conn.QueryRow(ctx, `SELECT on_, note FROM flags WHERE id = 'k'`).Scan(&k, &kNote))
	assert.True(t, k, "positional insert")
	assert.Equal(t, "true", kNote, "a text column in a positional insert")
	mustExec(t, conn, `DELETE FROM flags WHERE id = 'k'`)

	// And a text parameter compared with a boolean column finds the rows.
	res := conn.PgConn().ExecParams(ctx, `SELECT id FROM flags WHERE on_ = $1 ORDER BY id`,
		[][]byte{[]byte("true")}, nil, nil, nil).Read()
	require.NoError(t, res.Err)
	assert.Len(t, res.Rows, 5)
}
