package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnnestWithOrdinality expands a literal array to rows carrying a 1-based
// ordinality column.
func TestUnnestWithOrdinality(t *testing.T) {
	conn := connect(t, startServer(t))
	assert.Equal(t, []string{"10:1", "20:2", "30:3"}, queryColumn(t, conn,
		`SELECT v || ':' || n FROM unnest('{10,20,30}') WITH ORDINALITY AS t(v, n) ORDER BY n`, 0))
}

// TestPgProcPopulated checks pg_proc lists the functions we provide (so \df
// works and tools that introspect functions see them).
func TestPgProcPopulated(t *testing.T) {
	conn := connect(t, startServer(t))
	names := queryColumn(t, conn, `SELECT proname FROM pg_catalog.pg_proc ORDER BY proname`, 0)
	assert.NotEmpty(t, names)
	assert.Subset(t, names, []string{"version", "now", "age", "to_char"})
}

// TestInformationSchemaRoutines checks routines is populated from the same set.
func TestInformationSchemaRoutines(t *testing.T) {
	conn := connect(t, startServer(t))
	names := queryColumn(t, conn,
		`SELECT routine_name FROM information_schema.routines WHERE routine_schema = 'pg_catalog'`, 0)
	assert.Contains(t, names, "version")
	// routines exposes routine_type = 'FUNCTION'.
	types := queryColumn(t, conn,
		`SELECT DISTINCT routine_type FROM information_schema.routines`, 0)
	assert.Equal(t, []string{"FUNCTION"}, types)
}

// TestCompositeType records CREATE TYPE ... AS (...) in the catalog (typtype 'c').
func TestCompositeType(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TYPE addr AS (street text, zip text)`)

	assert.Equal(t, []string{"c"}, queryColumn(t, conn,
		`SELECT typtype FROM pg_catalog.pg_type WHERE typname = 'addr'`, 0))
	// It shows up in the type list (psql \dT).
	assert.Contains(t, queryColumn(t, conn,
		`SELECT typname FROM pg_catalog.pg_type WHERE typtype = 'c'`, 0), "addr")

	mustExec(t, conn, `DROP TYPE addr`)
	assert.NotContains(t, queryColumn(t, conn,
		`SELECT typname FROM pg_catalog.pg_type WHERE typtype = 'c'`, 0), "addr")
}

// TestArrayToStringOfSubquery covers array_to_string(array(SELECT ...), sep),
// which \dT+ uses to render an enum's element list.
func TestArrayToStringOfSubquery(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`)

	var elems string
	require.NoError(t, conn.QueryRow(context.Background(),
		`SELECT array_to_string(array(
			SELECT e.enumlabel FROM pg_catalog.pg_enum e
			JOIN pg_catalog.pg_type t ON t.oid = e.enumtypid
			WHERE t.typname = 'mood' ORDER BY e.enumsortorder), ', ')`).Scan(&elems))
	assert.Equal(t, "sad, ok, happy", elems)
}

// TestSQLPrepareExecute covers SQL-level PREPARE/EXECUTE/DEALLOCATE (used by
// psql and pg_dump).
func TestSQLPrepareExecute(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id int, name text)`)
	mustExec(t, conn, `INSERT INTO t VALUES (1,'a'),(2,'b'),(3,'c')`)

	mustExec(t, conn, `PREPARE byId (int) AS SELECT name FROM t WHERE id = $1`)
	var name string
	require.NoError(t, conn.QueryRow(ctx, `EXECUTE byId (2)`).Scan(&name))
	assert.Equal(t, "b", name)
	require.NoError(t, conn.QueryRow(ctx, `EXECUTE byId (3)`).Scan(&name))
	assert.Equal(t, "c", name)

	mustExec(t, conn, `DEALLOCATE byId`)
	_, err := conn.Exec(ctx, `EXECUTE byId (1)`)
	require.Error(t, err, "deallocated statement must be gone")
}

// TestAge computes a calendar interval between two timestamps.
func TestAge(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var s string
	require.NoError(t, conn.QueryRow(ctx, `SELECT age('2020-03-15', '2019-01-10')`).Scan(&s))
	assert.Equal(t, "1 year 2 mons 5 days", s)

	require.NoError(t, conn.QueryRow(ctx, `SELECT age('2020-03-05', '2019-01-10')`).Scan(&s))
	assert.Equal(t, "1 year 1 mon 24 days", s)

	require.NoError(t, conn.QueryRow(ctx, `SELECT age('2020-01-10', '2020-01-10')`).Scan(&s))
	assert.Equal(t, "00:00:00", s)
}
