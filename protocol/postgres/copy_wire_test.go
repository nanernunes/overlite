package postgres_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopyFromStdinText(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id INTEGER, nome TEXT, obs TEXT)`)

	// Text format: tab-delimited, \N for NULL.
	data := strings.NewReader("1\tana\tvip\n2\tbob\t\\N\n")
	tag, err := conn.PgConn().CopyFrom(ctx, data, `COPY t (id, nome, obs) FROM STDIN`)
	require.NoError(t, err)
	assert.EqualValues(t, 2, tag.RowsAffected())

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)

	var nome string
	var obs *string
	require.NoError(t, conn.QueryRow(ctx, `SELECT nome, obs FROM t WHERE id = 2`).Scan(&nome, &obs))
	assert.Equal(t, "bob", nome)
	assert.Nil(t, obs) // \N became NULL
}

func TestCopyToStdoutText(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id INTEGER, nome TEXT)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, 'ana'), (2, 'bob')`)

	var buf bytes.Buffer
	tag, err := conn.PgConn().CopyTo(ctx, &buf, `COPY t TO STDOUT`)
	require.NoError(t, err)
	assert.EqualValues(t, 2, tag.RowsAffected())
	assert.Equal(t, "1\tana\n2\tbob\n", buf.String())
}

func TestCopyCSVRoundTrip(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id INTEGER, nome TEXT)`)

	// Import CSV with header.
	data := strings.NewReader("id,nome\n1,ana\n2,\"bob, jr\"\n")
	_, err := conn.PgConn().CopyFrom(ctx, data, `COPY t (id, nome) FROM STDIN WITH (FORMAT csv, HEADER)`)
	require.NoError(t, err)

	var nome string
	require.NoError(t, conn.QueryRow(ctx, `SELECT nome FROM t WHERE id = 2`).Scan(&nome))
	assert.Equal(t, "bob, jr", nome) // quoted comma preserved

	// Export CSV.
	var buf bytes.Buffer
	_, err = conn.PgConn().CopyTo(ctx, &buf, `COPY t TO STDOUT WITH (FORMAT csv)`)
	require.NoError(t, err)
	assert.Contains(t, buf.String(), `"bob, jr"`)
}

// A round trip through TO STDOUT then FROM STDIN reproduces the data (the
// pg_dump pattern).
func TestCopyDumpRestore(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE src (id INTEGER, nome TEXT)`)
	mustExec(t, conn, `INSERT INTO src VALUES (1, 'ana'), (2, 'bob'), (3, 'carol')`)

	var dump bytes.Buffer
	_, err := conn.PgConn().CopyTo(ctx, &dump, `COPY src TO STDOUT`)
	require.NoError(t, err)

	mustExec(t, conn, `CREATE TABLE dst (id INTEGER, nome TEXT)`)
	tag, err := conn.PgConn().CopyFrom(ctx, &dump, `COPY dst FROM STDIN`)
	require.NoError(t, err)
	assert.EqualValues(t, 3, tag.RowsAffected())

	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM dst`).Scan(&n))
	assert.Equal(t, 3, n)
}
