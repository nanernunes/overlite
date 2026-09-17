package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"overlite/engine"
	"overlite/protocol/postgres"
	"overlite/server"
)

// startCluster serves a directory of databases, the way --db-dir does.
func startCluster(t *testing.T, maxOpen int) (addr, dir string) {
	t.Helper()
	dir = t.TempDir()

	cluster, err := engine.OpenDir(dir, maxOpen)
	require.NoError(t, err)

	srv, err := server.New("127.0.0.1:0", postgres.New(), cluster)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx)
	t.Cleanup(func() {
		cancel()
		srv.Close()
		cluster.Close()
	})
	return srv.Addr(), dir
}

func dialDB(t *testing.T, addr, database string) *pgx.Conn {
	t.Helper()
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://overlite@%s/%s?sslmode=disable", addr, database))
	require.NoError(t, err)
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		return nil
	}
	t.Cleanup(func() { conn.Close(context.Background()) })
	return conn
}

// Each database is its own file, and a connection sees only the one it asked
// for. This is the whole point of the arrangement: nothing in SQL reaches
// across, so one tenant cannot read another whatever a query says.
func TestDatabasePerFile(t *testing.T) {
	addr, dir := startCluster(t, 8)
	ctx := context.Background()

	admin := dialDB(t, addr, "postgres")
	require.Nil(t, admin, "connecting to a database that does not exist should fail")

	// Create two, then use them.
	first := mustCreateDatabase(t, addr, dir, "acme")
	second := mustCreateDatabase(t, addr, dir, "globex")

	mustExec(t, first, `CREATE TABLE incidents (id int primary key, title text)`)
	mustExec(t, first, `INSERT INTO incidents VALUES (1, 'acme incident')`)
	mustExec(t, second, `CREATE TABLE incidents (id int primary key, title text)`)
	mustExec(t, second, `INSERT INTO incidents VALUES (1, 'globex incident')`)

	var title string
	require.NoError(t, first.QueryRow(ctx, `SELECT title FROM incidents`).Scan(&title))
	assert.Equal(t, "acme incident", title)
	require.NoError(t, second.QueryRow(ctx, `SELECT title FROM incidents`).Scan(&title))
	assert.Equal(t, "globex incident", title)

	// current_database() answers per connection.
	var name string
	require.NoError(t, first.QueryRow(ctx, `SELECT current_database()`).Scan(&name))
	assert.Equal(t, "acme", name)
	require.NoError(t, second.QueryRow(ctx, `SELECT current_database()`).Scan(&name))
	assert.Equal(t, "globex", name)

	// A file per database, on disk.
	for _, n := range []string{"acme", "globex"} {
		assert.FileExists(t, filepath.Join(dir, n+".db"))
	}

	// Nothing in SQL crosses over.
	_, err := first.Exec(ctx, `SELECT * FROM globex.incidents`)
	assert.Error(t, err, "a query reached into another database")
}

func mustCreateDatabase(t *testing.T, addr, dir, name string) *pgx.Conn {
	t.Helper()
	// Any existing database can host the CREATE; the first one bootstraps from
	// a database created on disk by the cluster itself.
	if _, err := os.Stat(filepath.Join(dir, "bootstrap.db")); os.IsNotExist(err) {
		cluster, err := engine.OpenDir(dir, 8)
		require.NoError(t, err)
		require.NoError(t, cluster.CreateDatabase(context.Background(), "bootstrap"))
		cluster.Close()
	}
	boot := dialDB(t, addr, "bootstrap")
	require.NotNil(t, boot)
	mustExec(t, boot, `CREATE DATABASE `+name)
	conn := dialDB(t, addr, name)
	require.NotNilf(t, conn, "could not connect to the database just created: %s", name)
	return conn
}

// Asking for a database this server does not hold is an error naming what it
// does hold — not a silent landing on whichever database happens to be here.
func TestUnknownDatabaseIsRefused(t *testing.T) {
	addr, dir := startCluster(t, 8)
	cluster, err := engine.OpenDir(dir, 8)
	require.NoError(t, err)
	require.NoError(t, cluster.CreateDatabase(context.Background(), "shop"))
	cluster.Close()

	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://overlite@%s/nope?sslmode=disable", addr))
	require.NoError(t, err)
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = pgx.ConnectConfig(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `database "nope" does not exist`)
	assert.Contains(t, err.Error(), "shop", "the error should name what is available")
}

func TestCreateAndDropDatabase(t *testing.T) {
	addr, dir := startCluster(t, 8)
	ctx := context.Background()

	conn := mustCreateDatabase(t, addr, dir, "tenant_a")

	// pg_database lists what the server holds.
	boot := dialDB(t, addr, "bootstrap")
	names := func() []string {
		rows, err := boot.Query(ctx, `SELECT datname FROM pg_database ORDER BY datname`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var n string
			require.NoError(t, rows.Scan(&n))
			out = append(out, n)
		}
		return out
	}
	assert.Equal(t, []string{"bootstrap", "tenant_a"}, names())

	// Creating one that exists is an error; IF NOT EXISTS is not.
	_, err := boot.Exec(ctx, `CREATE DATABASE tenant_a`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already exists")
	_, err = boot.Exec(ctx, `CREATE DATABASE IF NOT EXISTS tenant_a`)
	require.NoError(t, err)

	// A database in use cannot be dropped.
	_, err = boot.Exec(ctx, `DROP DATABASE tenant_a`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "being accessed")

	conn.Close(ctx)
	// Give the server a moment to notice the connection went away.
	require.Eventually(t, func() bool {
		_, err := boot.Exec(ctx, `DROP DATABASE tenant_a`)
		return err == nil
	}, 3*time.Second, 50*time.Millisecond)

	assert.Equal(t, []string{"bootstrap"}, names())
	assert.NoFileExists(t, filepath.Join(dir, "tenant_a.db"))

	// And it is gone for a client too.
	assert.Nil(t, dialDB(t, addr, "tenant_a"))
}

// Far more databases than the cluster keeps open at once still all work: the
// idle ones are closed and reopened on demand.
func TestMoreDatabasesThanStayOpen(t *testing.T) {
	const total, maxOpen = 12, 3
	addr, dir := startCluster(t, maxOpen)
	ctx := context.Background()

	cluster, err := engine.OpenDir(dir, maxOpen)
	require.NoError(t, err)
	for i := 0; i < total; i++ {
		require.NoError(t, cluster.CreateDatabase(ctx, fmt.Sprintf("t%02d", i)))
	}
	cluster.Close()

	// Write to each in turn, which forces eviction and reopening.
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("t%02d", i)
		c := dialDB(t, addr, name)
		require.NotNilf(t, c, "could not reach %s", name)
		mustExec(t, c, `CREATE TABLE t (v text)`)
		_, err := c.Exec(ctx, `INSERT INTO t VALUES ($1)`, name)
		require.NoError(t, err)
		c.Close(ctx)
	}

	// Every one kept its own data.
	for i := 0; i < total; i++ {
		name := fmt.Sprintf("t%02d", i)
		c := dialDB(t, addr, name)
		require.NotNil(t, c)
		var v string
		require.NoError(t, c.QueryRow(ctx, `SELECT v FROM t`).Scan(&v))
		assert.Equal(t, name, v)
		c.Close(ctx)
	}
}
