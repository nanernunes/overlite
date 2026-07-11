package postgres_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDumpMachinery exercises the catalog features pg_dump relies on, at the
// wire level (so it runs without the pg_dump binary installed).
func TestDumpMachinery(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE t (id int PRIMARY KEY, code text)`)

	// tableoid is exposed on every catalog view.
	var toid int64
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT tableoid FROM pg_catalog.pg_class WHERE relname = 't'`).Scan(&toid))
	assert.EqualValues(t, 1259, toid)

	// unnest of a literal array expands into rows (pg_dump drives bulk fetches
	// this way), including the column-alias-list syntax.
	assert.Equal(t, []string{"10", "20", "30"}, queryColumn(t, conn,
		`SELECT x::text FROM unnest('{10,20,30}') AS t(x) ORDER BY x`, 0))

	// array_agg is a real aggregate building a Postgres array literal.
	var agg string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT array_agg(v ORDER BY v) FROM (SELECT 3 AS v UNION SELECT 1 UNION SELECT 2)`).Scan(&agg))
	assert.Equal(t, "{1,2,3}", agg)

	// pg_get_indexdef reconstructs a full CREATE INDEX.
	mustExec(t, conn, `CREATE INDEX t_code_idx ON t (code)`)
	var def string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT pg_get_indexdef(indexrelid) FROM pg_catalog.pg_index i
		 JOIN pg_catalog.pg_class c ON c.oid = i.indexrelid WHERE c.relname = 't_code_idx'`).Scan(&def))
	assert.Contains(t, def, "CREATE INDEX")
	assert.Contains(t, def, "USING btree (code)")
}

// TestPgDumpIntegration runs the real pg_dump against the server when the binary
// is available, asserting a usable schema dump.
func TestPgDumpIntegration(t *testing.T) {
	bin, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Skip("pg_dump not installed")
	}
	addr := startServer(t)
	conn := connect(t, addr)
	mustExec(t, conn, `CREATE TABLE dept (id int PRIMARY KEY, name text NOT NULL)`)
	mustExec(t, conn, `CREATE TABLE emp (
		id int PRIMARY KEY, dept_id int REFERENCES dept(id), active boolean DEFAULT true)`)
	mustExec(t, conn, `CREATE SEQUENCE order_seq START WITH 100 INCREMENT BY 5`)

	host, port, ok := strings.Cut(addr, ":")
	require.True(t, ok)
	out, err := exec.Command(bin, "-h", host, "-p", port, "-U", "postgres", "-d", "test",
		"--schema-only").CombinedOutput()
	require.NoErrorf(t, err, "pg_dump failed: %s", out)

	dump := string(out)
	assert.Contains(t, dump, "CREATE TABLE public.dept")
	assert.Contains(t, dump, "id integer NOT NULL")             // NOT NULL
	assert.Contains(t, dump, "active boolean DEFAULT true")     // DEFAULT
	assert.Contains(t, dump, "ADD CONSTRAINT emp_pkey PRIMARY KEY (id)") // PK constraint
	assert.Contains(t, dump, "FOREIGN KEY (dept_id) REFERENCES dept(id)")
	assert.Contains(t, dump, "CREATE SEQUENCE public.order_seq") // sequence
	assert.Contains(t, dump, "INCREMENT BY 5")
}
