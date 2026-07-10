package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Representative of the introspection queries DBeaver's PostgreSQL connector
// (over pgjdbc) runs while connecting and expanding the object tree: roles,
// databases, schemas (with descriptions and privilege filtering), and types.
// They must execute so the connection and navigator tree build.

const dbeaverRoles = `SELECT r.oid, r.rolname, r.rolsuper, r.rolinherit, r.rolcreaterole,
  r.rolcreatedb, r.rolcanlogin, r.rolreplication, r.rolbypassrls, r.rolconnlimit
FROM pg_catalog.pg_roles r
ORDER BY r.rolname;`

const dbeaverDatabases = `SELECT db.oid, db.datname, db.datistemplate, db.datallowconn,
  pg_catalog.pg_get_userbyid(db.datdba) AS owner,
  pg_catalog.pg_encoding_to_char(db.encoding) AS encoding,
  pg_catalog.has_database_privilege(db.oid, 'CONNECT') AS accessible
FROM pg_catalog.pg_database db
WHERE db.datallowconn
ORDER BY db.datname;`

const dbeaverSchemas = `SELECT n.oid, n.nspname, n.nspowner,
  pg_catalog.pg_get_userbyid(n.nspowner) AS owner,
  d.description
FROM pg_catalog.pg_namespace n
  LEFT OUTER JOIN pg_catalog.pg_description d ON d.objoid = n.oid AND d.objsubid = 0
WHERE pg_catalog.has_schema_privilege(n.oid, 'USAGE')
  AND n.nspname NOT LIKE 'pg_temp_%'
ORDER BY n.nspname;`

const dbeaverTypes = `SELECT t.oid, t.typname, t.typtype, t.typcategory, t.typlen,
  t.typbasetype, t.typelem, t.typrelid, t.typnamespace
FROM pg_catalog.pg_type t
ORDER BY t.oid;`

const dbeaverTables = `SELECT c.oid, c.relname, c.relkind, c.relnamespace, c.relpersistence,
  c.reltablespace, pg_catalog.pg_get_userbyid(c.relowner) AS owner,
  d.description
FROM pg_catalog.pg_class c
  LEFT OUTER JOIN pg_catalog.pg_description d ON d.objoid = c.oid AND d.objsubid = 0
WHERE c.relnamespace = 2200 AND c.relkind IN ('r','v','m','f','p')
  AND pg_catalog.has_table_privilege(c.oid, 'SELECT')
ORDER BY c.relname;`

func TestDBeaverConnectQueries(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)

	// Each connection-time query must execute without error.
	for _, tc := range []struct {
		name, query string
	}{
		{"roles", dbeaverRoles},
		{"databases", dbeaverDatabases},
		{"schemas", dbeaverSchemas},
		{"types", dbeaverTypes},
		{"tables", dbeaverTables},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := conn.Query(context.Background(), tc.query)
			require.NoErrorf(t, err, "DBeaver %s query must execute", tc.name)
			defer rows.Close()
			for rows.Next() {
			}
			require.NoError(t, rows.Err())
		})
	}
}

func TestDBeaverSchemaQualifiedData(t *testing.T) {
	// DBeaver reads table data with the schema-qualified name public.<table>;
	// SQLite has no "public" schema, so the qualifier must be dropped.
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)
	mustExec(t, conn, `INSERT INTO clientes (nome) VALUES ('ana')`)

	var id int
	var nome string
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT id, nome FROM public.clientes ORDER BY id LIMIT 1`).Scan(&id, &nome))
	assert.Equal(t, 1, id)
	assert.Equal(t, "ana", nome)

	// Quoted form too.
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT nome FROM "public"."clientes" WHERE id = 1`).Scan(&nome))
	assert.Equal(t, "ana", nome)
}

func TestDBeaverRolesAndSchemasContent(t *testing.T) {
	conn := connect(t, startServer(t))

	roles := queryColumn(t, conn, dbeaverRoles, 1)
	assert.Contains(t, roles, "postgres") // default role follows POSTGRES_USER

	schemas := queryColumn(t, conn, dbeaverSchemas, 1)
	assert.Contains(t, schemas, "public")

	dbs := queryColumn(t, conn, dbeaverDatabases, 1)
	assert.Contains(t, dbs, "test") // database name derives from the file (test.db)
}
