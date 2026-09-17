package postgres_test

import (
	"context"
	"fmt"
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

// entryDatabase is the database a test server is started with: the file it was
// pointed at, always there to connect to.
const entryDatabase = "postgres"

// startCluster serves a directory of databases, the way pointing overlite at a
// file does.
func startCluster(t *testing.T, maxOpen int) (addr, dir string) {
	t.Helper()
	dir = t.TempDir()

	cluster, err := engine.OpenCluster(filepath.Join(dir, entryDatabase+".db"), maxOpen)
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

	// The database the server was started with is always there to connect to.
	admin := dialDB(t, addr, entryDatabase)
	require.NotNil(t, admin)

	first := mustCreateDatabase(t, addr, admin, "acme")
	second := mustCreateDatabase(t, addr, admin, "globex")

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

// mustCreateDatabase creates a database through an open connection and returns
// a connection to it.
func mustCreateDatabase(t *testing.T, addr string, admin *pgx.Conn, name string) *pgx.Conn {
	t.Helper()
	mustExec(t, admin, `CREATE DATABASE `+name)
	conn := dialDB(t, addr, name)
	require.NotNilf(t, conn, "could not connect to the database just created: %s", name)
	return conn
}

// Asking for a database this server does not hold is an error naming what it
// does hold — not a silent landing on whichever database happens to be here.
func TestUnknownDatabaseIsRefused(t *testing.T) {
	addr, _ := startCluster(t, 8)
	admin := dialDB(t, addr, entryDatabase)
	require.NotNil(t, admin)
	mustExec(t, admin, `CREATE DATABASE shop`)

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

	boot := dialDB(t, addr, entryDatabase)
	require.NotNil(t, boot)
	conn := mustCreateDatabase(t, addr, boot, "tenant_a")

	// pg_database lists what the server holds.
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
	assert.Equal(t, []string{entryDatabase, "tenant_a"}, names())

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

	assert.Equal(t, []string{entryDatabase}, names())
	assert.NoFileExists(t, filepath.Join(dir, "tenant_a.db"))

	// And it is gone for a client too.
	assert.Nil(t, dialDB(t, addr, "tenant_a"))
}

// Far more databases than the cluster keeps open at once still all work: the
// idle ones are closed and reopened on demand.
func TestMoreDatabasesThanStayOpen(t *testing.T) {
	const total, maxOpen = 12, 3
	addr, _ := startCluster(t, maxOpen)
	ctx := context.Background()

	admin := dialDB(t, addr, entryDatabase)
	require.NotNil(t, admin)
	for i := 0; i < total; i++ {
		mustExec(t, admin, fmt.Sprintf(`CREATE DATABASE t%02d`, i))
	}
	admin.Close(ctx)

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

// The file overlite is pointed at brings its directory with it: a database
// created from a connection lands beside it, and is reachable by name. It is
// also the one database that is always there, so it cannot be dropped.
func TestEntryDatabaseBringsItsDirectory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shop.db")

	cluster, err := engine.OpenCluster(path, 8)
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
	addr := srv.Addr()

	// Pointing at a path that did not exist creates it, so there is something
	// to connect to.
	assert.FileExists(t, path)

	shop := dialDB(t, addr, "shop")
	require.NotNil(t, shop, "the database the server was started with is not reachable")

	// A database created from here lands beside the file.
	mustExec(t, shop, `CREATE DATABASE warehouse`)
	assert.FileExists(t, filepath.Join(dir, "warehouse.db"))

	warehouse := dialDB(t, addr, "warehouse")
	require.NotNil(t, warehouse)
	mustExec(t, warehouse, `CREATE TABLE t (v text)`)
	_, err = warehouse.Exec(context.Background(), `INSERT INTO t VALUES ('kept')`)
	require.NoError(t, err)

	// The entry database cannot be dropped: it is the way back in.
	_, err = warehouse.Exec(context.Background(), `DROP DATABASE shop`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "started with")
}

// A database already sitting beside the file is served without being created
// through overlite, which is what makes the directory the unit.
func TestExistingSiblingIsADatabase(t *testing.T) {
	dir := t.TempDir()

	// A database written before the server ever starts.
	pre, err := engine.Open(filepath.Join(dir, "legacy.db"))
	require.NoError(t, err)
	_, err = pre.Execute(context.Background(), `CREATE TABLE t (v text)`, nil)
	require.NoError(t, err)
	_, err = pre.Execute(context.Background(), `INSERT INTO t VALUES ('from before')`, nil)
	require.NoError(t, err)
	pre.Close()

	cluster, err := engine.OpenCluster(filepath.Join(dir, "main.db"), 8)
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

	conn := dialDB(t, srv.Addr(), "legacy")
	require.NotNil(t, conn, "a database beside the entry file was not served")

	var v string
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT v FROM t`).Scan(&v))
	assert.Equal(t, "from before", v)
}
