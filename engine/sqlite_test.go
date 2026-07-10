package engine

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestEngine opens a fresh on-disk SQLite engine in a temp dir. On-disk (not
// :memory:) so tests exercise the same WAL path production uses.
func newTestEngine(t *testing.T) *SQLite {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	eng, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { eng.Close() })
	return eng
}

func TestExecCreateInsertSelect(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	_, err := eng.Execute(ctx, `CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT, age INTEGER)`, nil)
	require.NoError(t, err)

	ins, err := eng.Execute(ctx, `INSERT INTO users (name, age) VALUES (?, ?)`, []any{"alice", int64(30)})
	require.NoError(t, err)
	assert.False(t, ins.IsQuery, "insert is not a query")
	assert.Equal(t, int64(1), ins.RowsAffected)
	assert.Equal(t, int64(1), ins.LastInsertID)
	assert.Equal(t, "INSERT", ins.Command)

	sel, err := eng.Execute(ctx, `SELECT id, name, age FROM users`, nil)
	require.NoError(t, err)
	assert.True(t, sel.IsQuery, "select is a query")
	require.Len(t, sel.Columns, 3)
	assert.Equal(t, "name", sel.Columns[1].Name)

	require.Len(t, sel.Rows, 1)
	row := sel.Rows[0]
	assert.Equal(t, int64(1), row[0])
	assert.Equal(t, "alice", asString(row[1]))
	assert.Equal(t, int64(30), row[2])
}

func TestExecArgsAndAffected(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	mustExec(t, eng, `CREATE TABLE t (n INTEGER)`)
	for i := 1; i <= 3; i++ {
		_, err := eng.Execute(ctx, `INSERT INTO t (n) VALUES (?)`, []any{int64(i)})
		require.NoError(t, err)
	}

	upd, err := eng.Execute(ctx, `UPDATE t SET n = n + 10 WHERE n >= ?`, []any{int64(2)})
	require.NoError(t, err)
	assert.Equal(t, int64(2), upd.RowsAffected)
	assert.Equal(t, "UPDATE", upd.Command)
}

func TestNullRoundTrip(t *testing.T) {
	eng := newTestEngine(t)
	ctx := context.Background()

	mustExec(t, eng, `CREATE TABLE t (a TEXT, b INTEGER)`)
	mustExec(t, eng, `INSERT INTO t (a, b) VALUES (NULL, NULL)`)

	sel, err := eng.Execute(ctx, `SELECT a, b FROM t`, nil)
	require.NoError(t, err)
	assert.Nil(t, sel.Rows[0][0])
	assert.Nil(t, sel.Rows[0][1])
}

func TestIsQueryHeuristic(t *testing.T) {
	cases := map[string]bool{
		"SELECT 1":                             true,
		"  select * from t":                    true,
		"WITH x AS (SELECT 1) SELECT * FROM x": true,
		"PRAGMA table_info(t)":                 true,
		"INSERT INTO t VALUES (1)":             false,
		"UPDATE t SET a = 1":                   false,
		"DELETE FROM t":                        false,
		"CREATE TABLE t (a INT)":               false,
		"INSERT INTO t VALUES (1) RETURNING a": true,
	}
	for sql, want := range cases {
		assert.Equalf(t, want, isQuery(sql), "isQuery(%q)", sql)
	}
}

func TestLeadingCommand(t *testing.T) {
	cases := map[string]string{
		"select 1":                 "SELECT",
		"insert into t values (1)": "INSERT",
		"create table t (a int)":   "CREATE TABLE",
		"drop table t":             "DROP TABLE",
	}
	for sql, want := range cases {
		assert.Equalf(t, want, leadingCommand(sql), "leadingCommand(%q)", sql)
	}
}

func mustExec(t *testing.T, eng *SQLite, sql string) {
	t.Helper()
	_, err := eng.Execute(context.Background(), sql, nil)
	require.NoErrorf(t, err, "exec %q", sql)
}

// asString normalizes the driver's TEXT representation (string or []byte).
func asString(v any) string {
	switch s := v.(type) {
	case string:
		return s
	case []byte:
		return string(s)
	default:
		return ""
	}
}
