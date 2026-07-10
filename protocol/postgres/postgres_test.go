package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"overlite/engine"
	"overlite/protocol/postgres"
	"overlite/server"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// startServer spins up a real overlite server (postgres protocol + on-disk
// SQLite) on a random port and returns its address.
func startServer(t *testing.T) string {
	t.Helper()

	eng, err := engine.Open(t.TempDir() + "/test.db")
	require.NoError(t, err)

	srv, err := server.New("127.0.0.1:0", postgres.New(), eng)
	if err != nil {
		eng.Close()
		require.NoError(t, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
		eng.Close()
	})
	return srv.Addr()
}

// connect dials the server with pgx. We pin the simple-query protocol because
// that is all the server implements today; the extended protocol is the next
// increment (see TestExtendedProtocol).
func connect(t *testing.T, addr string) *pgx.Conn {
	t.Helper()

	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://overlite@%s/main?sslmode=disable", addr))
	require.NoError(t, err)
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

func TestConnectAndSelectConstant(t *testing.T) {
	conn := connect(t, startServer(t))

	var n int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT 1").Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreateInsertSelectOverWire(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`)
	require.NoError(t, err)

	tag, err := conn.Exec(ctx, `INSERT INTO users (name, age) VALUES ('alice', 30)`)
	require.NoError(t, err)
	assert.EqualValues(t, 1, tag.RowsAffected())

	_, err = conn.Exec(ctx, `INSERT INTO users (name, age) VALUES ('bob', 25)`)
	require.NoError(t, err)

	rows, err := conn.Query(ctx, `SELECT id, name, age FROM users ORDER BY id`)
	require.NoError(t, err)

	type user struct {
		id   int
		name string
		age  int
	}
	var got []user
	for rows.Next() {
		var u user
		require.NoError(t, rows.Scan(&u.id, &u.name, &u.age))
		got = append(got, u)
	}
	require.NoError(t, rows.Err())

	want := []user{{1, "alice", 30}, {2, "bob", 25}}
	assert.Equal(t, want, got)
}

func TestNullOverWire(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE t (a TEXT, b INTEGER)`)
	mustExec(t, conn, `INSERT INTO t (a, b) VALUES (NULL, 5)`)

	var a *string
	var b int
	require.NoError(t, conn.QueryRow(ctx, `SELECT a, b FROM t`).Scan(&a, &b))
	assert.Nil(t, a)
	assert.Equal(t, 5, b)
}

func TestQueryError(t *testing.T) {
	conn := connect(t, startServer(t))
	// A query against a missing table must surface as an error, and the
	// connection must remain usable afterwards.
	_, err := conn.Exec(context.Background(), `SELECT * FROM nope`)
	require.Error(t, err)

	var n int
	require.NoError(t, conn.QueryRow(context.Background(), "SELECT 42").Scan(&n),
		"connection must stay usable after a query error")
	assert.Equal(t, 42, n)
}

func mustExec(t *testing.T, conn *pgx.Conn, sql string) {
	t.Helper()
	_, err := conn.Exec(context.Background(), sql)
	require.NoErrorf(t, err, "exec %q", sql)
}
