package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// connectExtended dials with pgx in its default mode, which uses the Extended
// Query protocol (Parse/Bind/Describe/Execute/Sync) and prepared statements.
func connectExtended(t *testing.T, addr string) *pgx.Conn {
	t.Helper()

	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://overlite@%s/main?sslmode=disable", addr))
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestExtendedSelectConstant(t *testing.T) {
	conn := connectExtended(t, startServer(t))

	var n int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT 1").Scan(&n))
	assert.Equal(t, 1, n)
}

func TestExtendedParametrized(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO users (name, age) VALUES ($1, $2)`, "alice", 30)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO users (name, age) VALUES ($1, $2)`, "bob", 25)
	require.NoError(t, err)

	// Parametrized SELECT through the extended protocol.
	rows, err := conn.Query(ctx, `SELECT name, age FROM users WHERE age >= $1 ORDER BY age`, 26)
	require.NoError(t, err)

	type row struct {
		name string
		age  int
	}
	var got []row
	for rows.Next() {
		var r row
		require.NoError(t, rows.Scan(&r.name, &r.age))
		got = append(got, r)
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, []row{{"alice", 30}}, got)
}

func TestExtendedCast(t *testing.T) {
	// Honors the original breadcrumb: $1::int must work (needs dialect rewrite
	// of the Postgres :: cast into SQLite CAST()).
	conn := connectExtended(t, startServer(t))

	var n int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT $1::int + 1", 7).Scan(&n))
	assert.Equal(t, 8, n)
}

func TestExtendedPreparedReuse(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	// Explicitly prepare, then execute several times: exercises statement
	// caching / reuse across Bind+Execute cycles.
	_, err := conn.Prepare(ctx, "double", "SELECT $1 * 2")
	require.NoError(t, err)

	for _, in := range []int{1, 5, 21} {
		var out int
		require.NoError(t, conn.QueryRow(ctx, "double", in).Scan(&out))
		assert.Equal(t, in*2, out)
	}
}

func TestExtendedInsertReturning(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT)`)
	require.NoError(t, err)

	var id int
	var v string
	err = conn.QueryRow(ctx, `INSERT INTO t (v) VALUES ($1) RETURNING id, v`, "hello").Scan(&id, &v)
	require.NoError(t, err)
	assert.Equal(t, 1, id)
	assert.Equal(t, "hello", v)
}
