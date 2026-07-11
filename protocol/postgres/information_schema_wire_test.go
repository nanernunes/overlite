package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInformationSchemaConstraints(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE dept (id int PRIMARY KEY, name text UNIQUE)`)
	mustExec(t, conn, `CREATE TABLE emp (
		id int PRIMARY KEY, dept_id int REFERENCES dept(id), email text NOT NULL)`)

	// table_constraints lists PK, FK and UNIQUE with Postgres-style names.
	assert.Equal(t, []string{
		"dept:PRIMARY KEY:dept_pkey",
		"dept:UNIQUE:dept_name_key",
		"emp:FOREIGN KEY:fk_emp_0",
		"emp:PRIMARY KEY:emp_pkey",
	}, queryColumn(t, conn, `SELECT table_name || ':' || constraint_type || ':' || constraint_name
		FROM information_schema.table_constraints WHERE table_schema = 'public'
		ORDER BY table_name, constraint_type`, 0))

	// key_column_usage maps constraints to their columns.
	assert.Equal(t, []string{"dept_pkey:id", "emp_pkey:id", "fk_emp_0:dept_id"},
		queryColumn(t, conn, `SELECT constraint_name || ':' || column_name
			FROM information_schema.key_column_usage
			WHERE constraint_name IN ('dept_pkey','emp_pkey','fk_emp_0')
			ORDER BY constraint_name`, 0))

	// referential_constraints links the FK to the referenced key.
	var uniq, del string
	require.NoError(t, conn.QueryRow(ctx, `SELECT unique_constraint_name, delete_rule
		FROM information_schema.referential_constraints WHERE constraint_name = 'fk_emp_0'`).
		Scan(&uniq, &del))
	assert.Equal(t, "dept_pkey", uniq)
	assert.Equal(t, "NO ACTION", del)
}

func TestInformationSchemaColumns(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (
		id int PRIMARY KEY, name text NOT NULL, note text DEFAULT 'hi', doc jsonb, active boolean)`)

	type col struct{ name, typ, nullable, def string }
	got := map[string]col{}
	rows, err := conn.Query(context.Background(), `SELECT column_name, data_type, is_nullable,
		coalesce(column_default, '') FROM information_schema.columns WHERE table_name = 't'`)
	require.NoError(t, err)
	defer rows.Close()
	for rows.Next() {
		var c col
		require.NoError(t, rows.Scan(&c.name, &c.typ, &c.nullable, &c.def))
		got[c.name] = c
	}
	require.NoError(t, rows.Err())

	assert.Equal(t, "integer", got["id"].typ)
	assert.Equal(t, "NO", got["id"].nullable) // primary key ⇒ not null
	assert.Equal(t, "NO", got["name"].nullable)
	assert.Equal(t, "YES", got["note"].nullable)
	assert.Equal(t, "'hi'", got["note"].def)
	assert.Equal(t, "jsonb", got["doc"].typ)
	assert.Equal(t, "boolean", got["active"].typ)
}

func TestInformationSchemaMisc(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (id int)`)
	mustExec(t, conn, `CREATE VIEW v AS SELECT id FROM t`)
	mustExec(t, conn, `CREATE SEQUENCE s START WITH 5`)

	assert.Subset(t, queryColumn(t, conn,
		`SELECT schema_name FROM information_schema.schemata`, 0),
		[]string{"public", "pg_catalog", "information_schema"})
	assert.Contains(t, queryColumn(t, conn,
		`SELECT table_name FROM information_schema.views WHERE table_schema = 'public'`, 0), "v")
	assert.Contains(t, queryColumn(t, conn,
		`SELECT sequence_name FROM information_schema.sequences`, 0), "s")
	// routines lists the provided functions; check_constraints is queryable (empty).
	assert.Contains(t, queryColumn(t, conn,
		`SELECT routine_name FROM information_schema.routines`, 0), "version")
	assert.Empty(t, queryColumn(t, conn,
		`SELECT constraint_name FROM information_schema.check_constraints`, 0))
}
