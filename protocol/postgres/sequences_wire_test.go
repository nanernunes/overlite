package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seqVal(t *testing.T, conn *pgx.Conn, q string) int64 {
	t.Helper()
	var n int64
	require.NoErrorf(t, conn.QueryRow(context.Background(), q).Scan(&n), "query %q", q)
	return n
}

func TestSequencesLifecycle(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// CREATE SEQUENCE: nextval starts at 1 and advances by 1.
	mustExec(t, conn, `CREATE SEQUENCE s1`)
	assert.EqualValues(t, 1, seqVal(t, conn, `SELECT nextval('s1')`))
	assert.EqualValues(t, 2, seqVal(t, conn, `SELECT nextval('s1')`))
	assert.EqualValues(t, 3, seqVal(t, conn, `SELECT nextval('s1')`))

	// currval / lastval report the session's last value.
	assert.EqualValues(t, 3, seqVal(t, conn, `SELECT currval('s1')`))
	assert.EqualValues(t, 3, seqVal(t, conn, `SELECT lastval()`))

	// setval sets the value; next nextval continues from there.
	assert.EqualValues(t, 100, seqVal(t, conn, `SELECT setval('s1', 100)`))
	assert.EqualValues(t, 101, seqVal(t, conn, `SELECT nextval('s1')`))
	// setval with is_called=false makes the next nextval return the value itself.
	seqVal(t, conn, `SELECT setval('s1', 200, false)`)
	assert.EqualValues(t, 200, seqVal(t, conn, `SELECT nextval('s1')`))

	// START WITH / INCREMENT BY.
	mustExec(t, conn, `CREATE SEQUENCE s2 START WITH 10 INCREMENT BY 5`)
	assert.EqualValues(t, 10, seqVal(t, conn, `SELECT nextval('s2')`))
	assert.EqualValues(t, 15, seqVal(t, conn, `SELECT nextval('s2')`))

	// ALTER SEQUENCE ... RESTART.
	mustExec(t, conn, `ALTER SEQUENCE s2 RESTART WITH 100`)
	assert.EqualValues(t, 100, seqVal(t, conn, `SELECT nextval('s2')`))

	// nextval embedded in an INSERT stores plain integers (file stays standalone).
	mustExec(t, conn, `CREATE TABLE items (id bigint, name text)`)
	mustExec(t, conn, `CREATE SEQUENCE items_seq`)
	mustExec(t, conn, `INSERT INTO items (id, name) VALUES (nextval('items_seq'), 'a')`)
	mustExec(t, conn, `INSERT INTO items (id, name) VALUES (nextval('items_seq'), 'b')`)
	assert.Equal(t, []string{"1", "2"}, queryColumn(t, conn, `SELECT id::text FROM items ORDER BY id`, 0))

	// IF NOT EXISTS is a no-op on an existing sequence.
	mustExec(t, conn, `CREATE SEQUENCE IF NOT EXISTS s1`)

	// DROP SEQUENCE.
	mustExec(t, conn, `DROP SEQUENCE s1`)
	_, err := conn.Exec(ctx, `SELECT nextval('s1')`)
	require.Error(t, err, "nextval on a dropped sequence must fail")
	mustExec(t, conn, `DROP SEQUENCE IF EXISTS s1`) // IF EXISTS swallows the missing one

	// The internal bookkeeping table stays hidden from the catalog.
	for _, rel := range queryColumn(t, conn,
		`SELECT relname FROM pg_catalog.pg_class WHERE relkind = 'r'`, 0) {
		assert.False(t, strings.HasPrefix(rel, "_overlite"), "internal table %q leaked", rel)
	}
}

// TestSequencePersistsAcrossConnections verifies the counter lives in the file,
// so a new connection continues where the previous one left off.
func TestSequencePersistsAcrossConnections(t *testing.T) {
	addr := startServer(t)
	c1 := connect(t, addr)
	mustExec(t, c1, `CREATE SEQUENCE p`)
	assert.EqualValues(t, 1, seqVal(t, c1, `SELECT nextval('p')`))
	assert.EqualValues(t, 2, seqVal(t, c1, `SELECT nextval('p')`))

	c2 := connect(t, addr)
	assert.EqualValues(t, 3, seqVal(t, c2, `SELECT nextval('p')`))
}

// TestSequencesExtendedProtocol exercises the Parse/Bind/Execute path used by
// JDBC/DBeaver: CREATE SEQUENCE and nextval must work through prepared portals.
func TestSequencesExtendedProtocol(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SEQUENCE e START WITH 5`)
	assert.EqualValues(t, 5, seqVal(t, conn, `SELECT nextval('e')`))
	assert.EqualValues(t, 6, seqVal(t, conn, `SELECT nextval('e')`))

	mustExec(t, conn, `CREATE TABLE t (id bigint, v int)`)
	_, err := conn.Exec(ctx, `INSERT INTO t (id, v) VALUES (nextval('e'), $1)`, 42)
	require.NoError(t, err)
	assert.EqualValues(t, 7, seqVal(t, conn, `SELECT id FROM t`))
}

// TestSequenceCatalogVisibility checks a sequence shows up in the catalog the
// way psql's \ds and \d <seq> query it.
func TestSequenceCatalogVisibility(t *testing.T) {
	conn := connect(t, startServer(t))

	mustExec(t, conn, `CREATE SEQUENCE cat_seq START WITH 7 INCREMENT BY 2 MAXVALUE 99 CYCLE`)

	// pg_class exposes it as relkind 'S' in the public schema.
	assert.Contains(t, queryColumn(t, conn,
		`SELECT relname FROM pg_catalog.pg_class WHERE relkind = 'S'`, 0), "cat_seq")

	// pg_sequence carries the parameters (joined by oid to pg_class).
	var start, incr, max, cycle int64
	require.NoError(t, conn.QueryRow(context.Background(),
		`SELECT s.seqstart, s.seqincrement, s.seqmax, s.seqcycle
		 FROM pg_catalog.pg_sequence s JOIN pg_catalog.pg_class c ON c.oid = s.seqrelid
		 WHERE c.relname = 'cat_seq'`).Scan(&start, &incr, &max, &cycle))
	assert.EqualValues(t, 7, start)
	assert.EqualValues(t, 2, incr)
	assert.EqualValues(t, 99, max)
	assert.EqualValues(t, 1, cycle)
}

func TestCurrvalBeforeNextvalErrors(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE SEQUENCE c`)
	_, err := conn.Exec(context.Background(), `SELECT currval('c')`)
	require.Error(t, err, "currval before nextval in the session must error")
}
