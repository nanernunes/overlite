package postgres_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This file pins the exact SQL psql (v16, via `psql -E`) sends for its
// backslash commands. Running them against overlite is what proves \dt and \d
// work, so we discover missing catalog objects / dialect gaps here in CI
// instead of by hand at a psql prompt.

// psqlListTables is the query behind \dt.
const psqlListTables = `SELECT n.nspname as "Schema",
  c.relname as "Name",
  CASE c.relkind WHEN 'r' THEN 'table' WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view' WHEN 'i' THEN 'index' WHEN 'S' THEN 'sequence' WHEN 't' THEN 'TOAST table' WHEN 'f' THEN 'foreign table' WHEN 'p' THEN 'partitioned table' WHEN 'I' THEN 'partitioned index' END as "Type",
  pg_catalog.pg_get_userbyid(c.relowner) as "Owner"
FROM pg_catalog.pg_class c
     LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     LEFT JOIN pg_catalog.pg_am am ON am.oid = c.relam
WHERE c.relkind IN ('r','p','t','')
      AND n.nspname <> 'pg_catalog'
      AND n.nspname !~ '^pg_toast'
      AND n.nspname <> 'information_schema'
  AND pg_catalog.pg_table_is_visible(c.oid)
ORDER BY 1,2;`

// psqlResolveRelation is the first query \d runs to find the table's oid.
const psqlResolveRelation = `SELECT c.oid,
  n.nspname,
  c.relname
FROM pg_catalog.pg_class c
     LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relname OPERATOR(pg_catalog.~) '^(%s)$' COLLATE pg_catalog.default
  AND pg_catalog.pg_table_is_visible(c.oid)
ORDER BY 2, 3;`

// psqlDescribeQueries are the follow-up queries \d runs once it has the oid.
// %[1]d is the relation oid.
var psqlDescribeQueries = []string{
	// relation details
	`SELECT c.relchecks, c.relkind, c.relhasindex, c.relhasrules, c.relhastriggers, c.relrowsecurity, c.relforcerowsecurity, false AS relhasoids, c.relispartition, '', c.reltablespace, CASE WHEN c.reloftype = 0 THEN '' ELSE c.reloftype::pg_catalog.regtype::pg_catalog.text END, c.relpersistence, c.relreplident, am.amname
FROM pg_catalog.pg_class c
 LEFT JOIN pg_catalog.pg_am am ON (c.relam = am.oid)
WHERE c.oid = '%[1]d';`,

	// columns
	`SELECT a.attname,
  pg_catalog.format_type(a.atttypid, a.atttypmod),
  (SELECT pg_catalog.pg_get_expr(d.adbin, d.adrelid, true)
   FROM pg_catalog.pg_attrdef d
   WHERE d.adrelid = a.attrelid AND d.adnum = a.attnum AND a.atthasdef),
  a.attnotnull,
  (SELECT c.collname FROM pg_catalog.pg_collation c, pg_catalog.pg_type t
   WHERE c.oid = a.attcollation AND t.oid = a.atttypid AND a.attcollation <> t.typcollation) AS attcollation,
  a.attidentity,
  a.attgenerated
FROM pg_catalog.pg_attribute a
WHERE a.attrelid = '%[1]d' AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum;`,

	// indexes
	`SELECT c2.relname, i.indisprimary, i.indisunique, i.indisclustered, i.indisvalid,
  pg_catalog.pg_get_indexdef(i.indexrelid, 0, true),
  pg_catalog.pg_get_constraintdef(con.oid, true), contype, condeferrable, condeferred, c2.reltablespace
FROM pg_catalog.pg_class c, pg_catalog.pg_class c2, pg_catalog.pg_index i
  LEFT JOIN pg_catalog.pg_constraint con ON (conrelid = i.indrelid AND conindid = i.indexrelid AND contype IN ('p','u','x'))
WHERE c.oid = '%[1]d' AND c.oid = i.indrelid AND i.indexrelid = c2.oid
ORDER BY i.indisprimary DESC, c2.relname;`,

	// check constraints
	`SELECT r.conname, pg_catalog.pg_get_constraintdef(r.oid, true)
FROM pg_catalog.pg_constraint r
WHERE r.conrelid = '%[1]d' AND r.contype = 'c'
ORDER BY 1;`,

	// foreign-key constraints
	`SELECT conname, pg_catalog.pg_get_constraintdef(r.oid, true) as condef
FROM pg_catalog.pg_constraint r
WHERE r.conrelid = '%[1]d' AND r.contype = 'f'
ORDER BY conname;`,

	// triggers
	`SELECT t.tgname, pg_catalog.pg_get_triggerdef(t.oid, true), t.tgenabled, t.tgisinternal
FROM pg_catalog.pg_trigger t
WHERE t.tgrelid = '%[1]d' AND NOT t.tgisinternal
ORDER BY 1;`,

	// rules
	`SELECT r.rulename, trim(trailing ';' from pg_catalog.pg_get_ruledef(r.oid, true))
FROM pg_catalog.pg_rewrite r
WHERE r.ev_class = '%[1]d' AND r.rulename != '_RETURN'
ORDER BY 1;`,

	// row-level security policies (exact psql 16 query; uses ARRAY(subquery)
	// and "= ANY(...)", which the dialect layer must rewrite)
	`SELECT pol.polname, pol.polpermissive,
  CASE WHEN pol.polroles = '{0}' THEN NULL ELSE pg_catalog.array_to_string(array(select rolname from pg_catalog.pg_roles where oid = any (pol.polroles) order by 1),',') END,
  pg_catalog.pg_get_expr(pol.polqual, pol.polrelid),
  pg_catalog.pg_get_expr(pol.polwithcheck, pol.polrelid),
  CASE pol.polcmd
    WHEN 'r' THEN 'SELECT'
    WHEN 'a' THEN 'INSERT'
    WHEN 'w' THEN 'UPDATE'
    WHEN 'd' THEN 'DELETE'
    END AS cmd
FROM pg_catalog.pg_policy pol
WHERE pol.polrelid = '%[1]d' ORDER BY 1;`,

	// child/partition relations
	`SELECT c.oid::pg_catalog.regclass
FROM pg_catalog.pg_class c, pg_catalog.pg_inherits i
WHERE c.oid = i.inhparent AND i.inhrelid = '%[1]d'
ORDER BY inhseqno;`,

	// publications
	`SELECT pubname
FROM pg_catalog.pg_publication p
JOIN pg_catalog.pg_publication_rel pr ON p.oid = pr.prpubid
WHERE pr.prrelid = '%[1]d'
ORDER BY 1;`,

	// referenced-by foreign keys (exact psql 16 query; regclass on a literal
	// stays an oid for the IN filter, on a column resolves to a name)
	`SELECT conname, conrelid::pg_catalog.regclass AS ontable,
       pg_catalog.pg_get_constraintdef(oid, true) AS condef
  FROM pg_catalog.pg_constraint c
 WHERE confrelid IN (SELECT pg_catalog.pg_partition_ancestors('%[1]d')
                     UNION ALL VALUES ('%[1]d'::pg_catalog.regclass))
       AND contype = 'f' AND conparentid = 0
ORDER BY conname;`,

	// extended statistics (exact psql 16 query)
	`SELECT oid, stxrelid::pg_catalog.regclass, stxnamespace::pg_catalog.regnamespace::pg_catalog.text AS nsp, stxname,
pg_catalog.pg_get_statisticsobjdef_columns(oid) AS columns,
  'd' = any(stxkind) AS ndist_enabled,
  'f' = any(stxkind) AS deps_enabled,
  'm' = any(stxkind) AS mcv_enabled,
stxstattarget
FROM pg_catalog.pg_statistic_ext
WHERE stxrelid = '%[1]d'
ORDER BY nsp, stxname;`,
}

func TestPsqlListTables(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)

	rows, err := conn.Query(context.Background(), psqlListTables)
	require.NoError(t, err, "psql \\dt query must execute")
	defer rows.Close()

	found := false
	for rows.Next() {
		vals, err := rows.Values()
		require.NoError(t, err)
		if vals[1] == "clientes" {
			found = true
		}
	}
	require.NoError(t, rows.Err())
	require.True(t, found, "clientes must show up in \\dt")
}

func TestPsqlDescribeTable(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)

	// Resolve the oid, as \d does first.
	var oid int64
	err := conn.QueryRow(context.Background(),
		fmt.Sprintf(psqlResolveRelation, "clientes")).Scan(&oid, new(string), new(string))
	require.NoError(t, err, "psql \\d relation-resolve query must execute and find the table")

	// Every follow-up query \d issues must execute without error.
	for i, q := range psqlDescribeQueries {
		runPsqlQuery(t, conn, i, fmt.Sprintf(q, oid))
	}
}

// --- other list commands (\l \dn \df \dv \di) ---------------------------------

const psqlListDatabases = `SELECT d.datname as "Name",
  pg_catalog.pg_get_userbyid(d.datdba) as "Owner",
  pg_catalog.pg_encoding_to_char(d.encoding) as "Encoding"
FROM pg_catalog.pg_database d
ORDER BY 1;`

const psqlListSchemas = `SELECT n.nspname AS "Name",
  pg_catalog.pg_get_userbyid(n.nspowner) AS "Owner"
FROM pg_catalog.pg_namespace n
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
ORDER BY 1;`

const psqlListFunctions = `SELECT n.nspname as "Schema",
  p.proname as "Name",
  pg_catalog.pg_get_function_result(p.oid) as "Result data type",
  pg_catalog.pg_get_function_arguments(p.oid) as "Argument data types"
FROM pg_catalog.pg_proc p
     LEFT JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
WHERE pg_catalog.pg_function_is_visible(p.oid)
      AND n.nspname <> 'pg_catalog' AND n.nspname <> 'information_schema'
ORDER BY 1, 2;`

const psqlListViews = `SELECT n.nspname as "Schema", c.relname as "Name",
  pg_catalog.pg_get_userbyid(c.relowner) as "Owner"
FROM pg_catalog.pg_class c
     LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('v','')
      AND n.nspname <> 'pg_catalog' AND n.nspname !~ '^pg_toast' AND n.nspname <> 'information_schema'
  AND pg_catalog.pg_table_is_visible(c.oid)
ORDER BY 1,2;`

const psqlListIndexes = `SELECT n.nspname as "Schema", c.relname as "Name",
  pg_catalog.pg_get_userbyid(c.relowner) as "Owner", c2.relname as "Table"
FROM pg_catalog.pg_class c
     LEFT JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
     LEFT JOIN pg_catalog.pg_index i ON i.indexrelid = c.oid
     LEFT JOIN pg_catalog.pg_class c2 ON i.indrelid = c2.oid
WHERE c.relkind IN ('i','I','')
      AND n.nspname <> 'pg_catalog' AND n.nspname !~ '^pg_toast' AND n.nspname <> 'information_schema'
  AND pg_catalog.pg_table_is_visible(c.oid)
ORDER BY 1,2;`

// namesOf runs q and returns the value of the "Name" column (index 1).
func queryColumn(t *testing.T, conn *pgx.Conn, q string, col int) []string {
	t.Helper()
	rows, err := conn.Query(context.Background(), q)
	require.NoErrorf(t, err, "query must execute:\n%s", q)
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		require.NoError(t, err)
		if s, ok := vals[col].(string); ok {
			out = append(out, s)
		}
	}
	require.NoError(t, rows.Err())
	return out
}

func TestPsqlListDatabases(t *testing.T) {
	conn := connect(t, startServer(t))
	assert.Contains(t, queryColumn(t, conn, psqlListDatabases, 0), "test") // db name from test.db
}

func TestPsqlListSchemas(t *testing.T) {
	conn := connect(t, startServer(t))
	assert.Equal(t, []string{"public"}, queryColumn(t, conn, psqlListSchemas, 0))
}

func TestPsqlListFunctions(t *testing.T) {
	// Must execute even though we expose no user functions (empty result).
	conn := connect(t, startServer(t))
	queryColumn(t, conn, psqlListFunctions, 1)
}

func TestPsqlListViews(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE base (a INTEGER)`)
	mustExec(t, conn, `CREATE VIEW ativos AS SELECT a FROM base WHERE a > 0`)
	assert.Contains(t, queryColumn(t, conn, psqlListViews, 1), "ativos")
}

func TestPsqlListIndexes(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT)`)
	mustExec(t, conn, `CREATE INDEX idx_nome ON clientes(nome)`)
	assert.Contains(t, queryColumn(t, conn, psqlListIndexes, 1), "idx_nome")
}

func TestPsqlDescribeShowsIndexAndForeignKey(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT)`)
	mustExec(t, conn, `CREATE TABLE pedidos (id INTEGER PRIMARY KEY, cliente_id INTEGER REFERENCES clientes(id))`)
	mustExec(t, conn, `CREATE INDEX idx_cli ON pedidos(cliente_id)`)

	var oid int64
	require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf(psqlResolveRelation, "pedidos")).
		Scan(&oid, new(string), new(string)))

	// Index query (\d "Indexes:" section) must list our index.
	indexes := queryColumn(t, conn, fmt.Sprintf(psqlDescribeQueries[2], oid), 0)
	assert.Contains(t, indexes, "idx_cli")

	// Foreign-key query (\d "Foreign-key constraints:") must return a row.
	rows, err := conn.Query(ctx, fmt.Sprintf(psqlDescribeQueries[4], oid))
	require.NoError(t, err)
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, 1, count, "pedidos has one foreign key")
}

func TestPsqlReferencedByRendersName(t *testing.T) {
	// The "Referenced by" section: the referencing table must render by NAME
	// (regclass on a column) while the IN filter (regclass on a literal oid)
	// still matches — the regclass heuristic must satisfy both.
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT)`)
	mustExec(t, conn, `CREATE TABLE pedidos (id INTEGER PRIMARY KEY, cliente_id INTEGER REFERENCES clientes(id))`)

	var oid int64
	require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf(psqlResolveRelation, "clientes")).
		Scan(&oid, new(string), new(string)))

	var conname, ontable, condef string
	require.NoError(t, conn.QueryRow(ctx, fmt.Sprintf(`
		SELECT conname, conrelid::pg_catalog.regclass AS ontable,
		       pg_catalog.pg_get_constraintdef(oid, true) AS condef
		FROM pg_catalog.pg_constraint c
		WHERE confrelid IN (SELECT pg_catalog.pg_partition_ancestors('%[1]d')
		                    UNION ALL VALUES ('%[1]d'::pg_catalog.regclass))
		  AND contype = 'f' AND conparentid = 0`, oid)).Scan(&conname, &ontable, &condef))

	assert.Equal(t, "pedidos", ontable) // name, not the oid
	assert.Contains(t, condef, "FOREIGN KEY (cliente_id) REFERENCES clientes(id)")
}

func runPsqlQuery(t *testing.T, conn *pgx.Conn, i int, q string) {
	t.Helper()
	rows, err := conn.Query(context.Background(), q)
	require.NoErrorf(t, err, "psql \\d sub-query #%d must execute:\n%s", i, q)
	defer rows.Close()
	for rows.Next() {
	}
	require.NoErrorf(t, rows.Err(), "psql \\d sub-query #%d row error:\n%s", i, q)
}
