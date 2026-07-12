package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Schemas map onto attached SQLite database files. The database overlite points
// at IS the "public" schema (the SQLite "main" database). Creating a schema
// "vendas" for a main file "system.db" creates and attaches "system.vendas.db".
//
// This file owns: deriving those paths, discovering existing schema files,
// attaching them per connection, and generating the catalog views that span
// every attached schema.

// schemaRef presents one Postgres schema to the catalog. In multi-file mode DB
// is the attached SQLite database and Prefix is empty. In single-file mode DB is
// always "main" and Prefix is the table-name prefix ("vendas.") that a schema's
// tables carry; public has an empty prefix.
type schemaRef struct {
	PgName string // "public", "vendas"
	DB     string // SQLite attached-db name: "main", "vendas"
	NsOid  int    // pg_namespace.oid
	Offset int64  // added to every oid so ids don't collide across schemas
	Prefix string // single-file table-name prefix: "" (public) or "vendas."
}

// master returns the SQL table source the per-schema templates read as
// @MASTER@. In multi-file mode it's the attached database's sqlite_master. In
// single-file mode the public schema reads a view of main.sqlite_master that
// excludes tables belonging to a registered schema (a "vendas." prefix), so
// those don't leak into public; non-public schemas don't drive templates.
func (r schemaRef) master() string {
	if schemaFilesMode {
		return r.DB + ".sqlite_master"
	}
	if r.Prefix == "" {
		// public: bare-named tables, i.e. anything not owned by a registered
		// schema (a "<schema>." prefix). mm.name must be qualified: an
		// unqualified `name` inside the EXISTS would bind to _overlite_schemas.
		return `(SELECT mm.rowid AS rowid, mm.type AS type, mm.name AS name,
		         mm.tbl_name AS tbl_name, mm.sql AS sql FROM main.sqlite_master mm
		  WHERE instr(mm.name,'.')=0
		     OR NOT EXISTS (SELECT 1 FROM _overlite_schemas s
		                    WHERE s.name = substr(mm.name,1,instr(mm.name,'.')-1)))`
	}
	// a schema: exactly the tables carrying its "<name>." prefix.
	return `(SELECT rowid, type, name, tbl_name, sql FROM main.sqlite_master
	  WHERE name LIKE '` + escapeLike(r.Prefix) + `%' ESCAPE '\')`
}

// catalogRole is the role name shown as owner / returned by current_user etc.
// It follows the official Postgres image's POSTGRES_USER (default "postgres").
// catalogDBName is the database name, derived from the file we point at.
var (
	catalogRole   = envOr("POSTGRES_USER", "postgres")
	catalogDBName = "main"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// dbNameFromPath derives the Postgres database name from the SQLite file path:
// "/data/hello.db" -> "hello".
func dbNameFromPath(path string) string {
	if path == "" || path == ":memory:" {
		return "main"
	}
	return strings.TrimSuffix(filepath.Base(path), ".db")
}

func sqlQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// metaCatalogViews are rebuilt per connection because they embed the (possibly
// runtime-derived) role and database name.
func metaCatalogViews() []string {
	return []string{
		// The configured role gets oid 10 (Postgres' bootstrap superuser oid), so
		// object owners (relowner=10 in pg_class) resolve to it for pg_dump.
		`CREATE TEMP VIEW pg_roles AS SELECT
		 CASE WHEN rolname = ` + sqlQuote(catalogRole) + ` COLLATE NOCASE THEN 10 ELSE rowid + 16384 END AS oid,
		 rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolcanlogin, rolreplication,
		 -1 AS rolconnlimit, '********' AS rolpassword, NULL AS rolvaliduntil,
		 rolbypassrls, NULL AS rolconfig, 1260 AS tableoid
		 FROM _overlite_roles`,
		`CREATE TEMP VIEW pg_sequences AS SELECT 'public' AS schemaname, seqname AS sequencename,
		 ` + sqlQuote(catalogRole) + ` AS sequenceowner, 'bigint' AS data_type,
		 start_value, min_value, max_value, increment AS increment_by, is_cycled AS cycle,
		 cache_size, CASE WHEN is_called THEN last_value ELSE NULL END AS last_value
		 FROM _overlite_sequences`,
		`CREATE TEMP VIEW pg_database AS SELECT 1 AS oid, ` + sqlQuote(catalogDBName) + ` AS datname,
		 10 AS datdba, 6 AS encoding, 'c' AS datlocprovider, 'C' AS datcollate, 'C' AS datctype,
		 NULL AS daticulocale, NULL AS daticurules, NULL AS datcollversion,
		 0 AS datistemplate, 1 AS datallowconn, -1 AS datconnlimit, 0 AS dattablespace,
		 0 AS datfrozenxid, 0 AS datminmxid, NULL AS datacl`,
	}
}

var reSchemaName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var reservedSchemas = map[string]bool{
	"main": true, "temp": true, "public": true,
	"pg_catalog": true, "information_schema": true,
}

func validSchemaName(name string) bool {
	return reSchemaName.MatchString(name) && !reservedSchemas[strings.ToLower(name)]
}

// mainDBPath extracts the database file path from a DSN like
// "file:system.db?_pragma=...".
func mainDBPath(dsn string) string {
	s := strings.TrimPrefix(dsn, "file:")
	if i := strings.IndexByte(s, '?'); i >= 0 {
		s = s[:i]
	}
	return s
}

// schemaFilePath returns the file backing schema `name` next to the main file:
// "system.db" + "vendas" -> "system.vendas.db".
func schemaFilePath(mainPath, name string) string {
	base := strings.TrimSuffix(mainPath, ".db")
	return base + "." + name + ".db"
}

// discoverSchemaFiles finds "<base>.<schema>.db" siblings of the main file.
func discoverSchemaFiles(mainPath string) map[string]string {
	out := map[string]string{}
	base := strings.TrimSuffix(mainPath, ".db")
	matches, _ := filepath.Glob(base + ".*.db")
	prefix := filepath.Base(base) + "."
	for _, m := range matches {
		name := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), prefix), ".db")
		if validSchemaName(name) { // single, valid segment only
			out[name] = m
		}
	}
	return out
}

// schemaRefs assigns stable oids/offsets to main (public) plus the given schema
// names (sorted for determinism). In multi-file mode each schema is its own
// attached database; in single-file mode all share "main" and carry a
// "<name>." table-name prefix.
func schemaRefs(schemas []string) []schemaRef {
	refs := []schemaRef{{PgName: "public", DB: "main", NsOid: 2200, Offset: 0}}
	sort.Strings(schemas)
	for i, name := range schemas {
		r := schemaRef{
			PgName: name,
			DB:     name,
			NsOid:  3000000 + i + 1,
			// Each schema gets a 200M-wide oid band. It must stay < 2^32 because
			// pg_dump holds oids in a uint32 and would truncate a larger value,
			// breaking its attrelid joins (empty CREATE TABLE). ~21 schemas fit.
			Offset: int64(i+1) * 200_000_000,
		}
		if !schemaFilesMode {
			r.DB = "main"
			r.Prefix = name + "."
		}
		refs = append(refs, r)
	}
	return refs
}

// --- per-connection setup -----------------------------------------------------

// setupConnection attaches every discovered schema file and (re)creates all
// catalog views spanning them. Called from the connection hook and after
// CREATE/DROP SCHEMA.
func setupConnection(ctx context.Context, exec func(string) error, query func(string) ([]string, error), mainPath string) error {
	var attached []string
	if schemaFilesMode {
		if mainPath != "" && mainPath != ":memory:" {
			for name, path := range discoverSchemaFiles(mainPath) {
				// Attach if not already attached; ignore "already in use".
				_ = exec(fmt.Sprintf("ATTACH DATABASE '%s' AS %q", path, name))
				attached = append(attached, name)
			}
		}
		// The set of attached schemas is the source of truth.
		if names, err := query("SELECT name FROM pragma_database_list WHERE name NOT IN ('main','temp')"); err == nil {
			attached = names
		}
	}
	// The schema registry (single-file mode reads it for the schema list; the
	// table exists in both modes so DDL is uniform).
	if err := exec(schemasTableDDL); err != nil {
		return err
	}
	if !schemaFilesMode {
		if names, err := query("SELECT name FROM _overlite_schemas ORDER BY name"); err == nil {
			attached = names
			setSchemaCache(names) // the schema-qualifier rewrite reads this
		}
	}

	// The internal roles table (pg_roles reads from it) must exist before the
	// meta views are built.
	if err := exec(rolesTableDDL); err != nil {
		return err
	}
	_ = exec(rolesAddPasswordDDL) // migrate older tables; errors if already present
	if err := exec(seedDefaultRoleSQL()); err != nil {
		return err
	}
	// The internal sequences table (nextval/currval/... read and write it).
	if err := exec(sequencesTableDDL); err != nil {
		return err
	}
	if err := exec(columnDefaultsTableDDL); err != nil {
		return err
	}
	// The internal enum tables (pg_type/pg_enum read them; enum columns become
	// TEXT + CHECK).
	if err := exec(enumTypesTableDDL); err != nil {
		return err
	}
	if err := exec(enumLabelsTableDDL); err != nil {
		return err
	}
	if err := exec(compositeTypesTableDDL); err != nil {
		return err
	}
	// COMMENT ON storage (pg_description reads it).
	if err := exec(commentsTableDDL); err != nil {
		return err
	}
	// LANGUAGE sql function definitions (inlined at call sites).
	if err := exec(functionsTableDDL); err != nil {
		return err
	}
	// The internal privilege tables (GRANT/REVOKE storage + table ownership),
	// consulted by the protocol before running a statement.
	if err := exec(grantsTableDDL); err != nil {
		return err
	}
	if err := exec(ownersTableDDL); err != nil {
		return err
	}
	if err := exec(membershipsTableDDL); err != nil {
		return err
	}
	_ = exec(membershipsAddAdminDDL) // migrate older tables; errors if present
	// Row-level security flags and policies.
	if err := exec(rlsTableDDL); err != nil {
		return err
	}
	if err := exec(policiesTableDDL); err != nil {
		return err
	}
	// Expose each sequence as a one-row relation (last_value/is_called), as
	// Postgres does, so pg_dump can read its current value with
	// "SELECT last_value, is_called FROM <seq>".
	if seqs, err := query("SELECT seqname FROM _overlite_sequences WHERE seqname NOT IN" +
		" (SELECT name FROM sqlite_master WHERE type IN ('table','view'))"); err == nil {
		for _, name := range seqs {
			_ = exec(`CREATE TEMP VIEW IF NOT EXISTS "` + strings.ReplaceAll(name, `"`, `""`) +
				`" AS SELECT last_value, 0 AS log_cnt, is_called FROM _overlite_sequences WHERE seqname = ` +
				sqlQuote(name))
		}
	}
	// Refresh the global enum oid->name registry that format_type() reads.
	if names, err := query("SELECT (rowid + 90000000) || ':' || typname FROM _overlite_enum_types"); err == nil {
		refreshEnumNames(names)
	}
	// Refresh the LANGUAGE sql function cache (columns joined by char(30)).
	if rows, err := query("SELECT name || char(30) || arity || char(30) || params || char(30) || body || char(30) || lang" +
		" FROM _overlite_functions"); err == nil {
		refreshFunctionsFromRows(rows)
	}
	// Refresh the trigger oid->definition registry that pg_get_triggerdef() reads
	// (public schema; oid matches the pg_trigger view).
	if defs, err := query("SELECT (rowid + 70000000) || char(31) || sql FROM sqlite_master" +
		" WHERE type = 'trigger' AND sql IS NOT NULL AND name NOT GLOB '_overlite_*'"); err == nil {
		refreshTriggerDefs(defs)
	}

	refs := schemaRefs(attached)
	for _, stmt := range staticCatalogViews {
		if err := exec(withTableOID(stmt)); err != nil {
			return err
		}
	}
	// Meta + schema-spanning views are rebuilt (DROP + CREATE) so they pick up
	// the current role/database name and schema set.
	rebuilt := append(metaCatalogViews(), dynamicCatalogViews(refs)...)
	for _, stmt := range rebuilt {
		if err := exec(dropViewOf(stmt)); err != nil {
			return err
		}
		if err := exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// dropViewOf returns a DROP VIEW statement for the view a CREATE statement
// defines, so we can rebuild dynamic views when the schema set changes.
func dropViewOf(createStmt string) string {
	name := viewName(createStmt)
	return "DROP VIEW IF EXISTS " + name
}

var reViewName = regexp.MustCompile(`(?i)CREATE\s+TEMP\s+VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?("[^"]+"|\w+)`)

func viewName(createStmt string) string {
	if m := reViewName.FindStringSubmatch(createStmt); m != nil {
		return m[1]
	}
	return ""
}

// --- schema management --------------------------------------------------------
//
// Schema DDL runs on a single connection (the engine's own, or a client
// session's), attaching/detaching the schema file and rebuilding that
// connection's catalog. Other connections pick the change up when they next
// connect (their hook re-discovers schema files from disk).

// connExecutor is what schema management needs from a *sql.Conn (or *sql.DB).
type connExecutor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func createSchema(ctx context.Context, ce connExecutor, mainPath, name string, ifNotExists bool) error {
	if strings.EqualFold(name, "public") {
		if ifNotExists {
			return nil
		}
		return fmt.Errorf("schema %q already exists", name)
	}
	if !validSchemaName(name) {
		return fmt.Errorf("invalid schema name %q", name)
	}
	if !schemaFilesMode {
		return createSchemaSingle(ctx, ce, mainPath, name, ifNotExists)
	}
	if mainPath == "" || mainPath == ":memory:" {
		return fmt.Errorf("schemas require an on-disk database")
	}
	if schemaAttached(ctx, ce, name) {
		if ifNotExists {
			return nil
		}
		return fmt.Errorf("schema %q already exists", name)
	}
	path := schemaFilePath(mainPath, name)
	if _, err := ce.ExecContext(ctx, fmt.Sprintf("ATTACH DATABASE '%s' AS %q", path, name)); err != nil {
		return err
	}
	return rebuildCatalog(ctx, ce, mainPath)
}

// createSchemaSingle records a schema in _overlite_schemas (single-file mode).
// It's an ordinary write, so it works inside a transaction and in :memory:.
func createSchemaSingle(ctx context.Context, ce connExecutor, mainPath, name string, ifNotExists bool) error {
	if _, err := ce.ExecContext(ctx, schemasTableDDL); err != nil {
		return err
	}
	if schemaRegistered(ctx, ce, name) {
		if ifNotExists {
			return nil
		}
		return fmt.Errorf("schema %q already exists", name)
	}
	if _, err := ce.ExecContext(ctx, "INSERT INTO _overlite_schemas (name) VALUES (?)", name); err != nil {
		return err
	}
	return rebuildCatalog(ctx, ce, mainPath)
}

func dropSchema(ctx context.Context, ce connExecutor, mainPath, name string, ifExists, cascade bool) error {
	if strings.EqualFold(name, "public") {
		return fmt.Errorf("cannot drop schema %q", name)
	}
	if !schemaFilesMode {
		return dropSchemaSingle(ctx, ce, mainPath, name, ifExists, cascade)
	}
	if !schemaAttached(ctx, ce, name) {
		if ifExists {
			return nil
		}
		return fmt.Errorf("schema %q does not exist", name)
	}
	if !cascade {
		var n int
		ce.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT count(*) FROM %q.sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%%'`, name)).Scan(&n)
		if n > 0 {
			return fmt.Errorf("schema %q is not empty (use CASCADE)", name)
		}
	}
	path := schemaFilePath(mainPath, name)
	if _, err := ce.ExecContext(ctx, fmt.Sprintf("DETACH DATABASE %q", name)); err != nil {
		return err
	}
	_ = os.Remove(path)
	_ = os.Remove(path + "-wal")
	_ = os.Remove(path + "-shm")
	return rebuildCatalog(ctx, ce, mainPath)
}

// dropSchemaSingle removes a schema and (with CASCADE) its prefixed tables in
// single-file mode. Fully transactional.
func dropSchemaSingle(ctx context.Context, ce connExecutor, mainPath, name string, ifExists, cascade bool) error {
	if !schemaRegistered(ctx, ce, name) {
		if ifExists {
			return nil
		}
		return fmt.Errorf("schema %q does not exist", name)
	}
	tables, err := schemaTables(ctx, ce, name)
	if err != nil {
		return err
	}
	if len(tables) > 0 && !cascade {
		return fmt.Errorf("schema %q is not empty (use CASCADE)", name)
	}
	for _, t := range tables {
		if _, err := ce.ExecContext(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %q", t)); err != nil {
			return err
		}
	}
	if _, err := ce.ExecContext(ctx, "DELETE FROM _overlite_schemas WHERE name = ? COLLATE NOCASE", name); err != nil {
		return err
	}
	return rebuildCatalog(ctx, ce, mainPath)
}

// schemaRegistered reports whether a schema is in the single-file registry.
func schemaRegistered(ctx context.Context, ce connExecutor, name string) bool {
	var found string
	err := ce.QueryRowContext(ctx,
		"SELECT name FROM _overlite_schemas WHERE name = ? COLLATE NOCASE", name).Scan(&found)
	return err == nil
}

// schemaTables lists the SQLite table names ("vendas.pedidos") belonging to a
// single-file schema.
func schemaTables(ctx context.Context, ce connExecutor, name string) ([]string, error) {
	rows, err := ce.QueryContext(ctx,
		"SELECT name FROM main.sqlite_master WHERE type='table' AND name LIKE ? ESCAPE '\\'",
		escapeLike(name)+".%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// escapeLike escapes LIKE metacharacters in a literal (used with ESCAPE '\').
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func schemaAttached(ctx context.Context, ce connExecutor, name string) bool {
	var found string
	err := ce.QueryRowContext(ctx,
		"SELECT name FROM pragma_database_list WHERE name = ?", name).Scan(&found)
	return err == nil
}

// rebuildCatalog re-runs the catalog setup on the given connection.
func rebuildCatalog(ctx context.Context, ce connExecutor, mainPath string) error {
	exec := func(q string) error {
		_, err := ce.ExecContext(ctx, q)
		return err
	}
	q := func(query string) ([]string, error) {
		rows, err := ce.QueryContext(ctx, query)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, rows.Err()
	}
	return setupConnection(ctx, exec, q, mainPath)
}

// SchemaDDLTransactional reports whether schema DDL can run inside a tx (yes in
// single-file mode, where a schema is an ordinary row/table, not an ATTACH).
func (s *SQLite) SchemaDDLTransactional() bool        { return !schemaFilesMode }
func (ss *sqliteSession) SchemaDDLTransactional() bool { return !schemaFilesMode }

// CreateSchema / DropSchema on the engine's own connection (tests, convenience).
func (s *SQLite) CreateSchema(ctx context.Context, name string, ifNotExists bool) error {
	return createSchema(ctx, s.conn, s.mainPath, name, ifNotExists)
}
func (s *SQLite) DropSchema(ctx context.Context, name string, ifExists, cascade bool) error {
	return dropSchema(ctx, s.conn, s.mainPath, name, ifExists, cascade)
}

// CreateSchema / DropSchema on a client session's connection.
func (ss *sqliteSession) CreateSchema(ctx context.Context, name string, ifNotExists bool) error {
	return createSchema(ctx, ss.conn, ss.mainPath, name, ifNotExists)
}
func (ss *sqliteSession) DropSchema(ctx context.Context, name string, ifExists, cascade bool) error {
	return dropSchema(ctx, ss.conn, ss.mainPath, name, ifExists, cascade)
}

func (s *SQLite) RenameSchema(ctx context.Context, o, n string) error {
	return renameSchema(ctx, s.conn, s.mainPath, o, n)
}
func (ss *sqliteSession) RenameSchema(ctx context.Context, o, n string) error {
	return renameSchema(ctx, ss.conn, ss.mainPath, o, n)
}
func (s *SQLite) SetTableSchema(ctx context.Context, ref, sch string) error {
	return setTableSchema(ctx, s.conn, s.mainPath, ref, sch)
}
func (ss *sqliteSession) SetTableSchema(ctx context.Context, ref, sch string) error {
	return setTableSchema(ctx, ss.conn, ss.mainPath, ref, sch)
}

// renameSchema renames a schema and every "old.<t>" table to "new.<t>" (single-
// file mode). A transactional set of renames.
func renameSchema(ctx context.Context, ce connExecutor, mainPath, oldName, newName string) error {
	if strings.EqualFold(oldName, "public") || strings.EqualFold(newName, "public") {
		return fmt.Errorf("cannot rename schema %q", "public")
	}
	if !validSchemaName(newName) {
		return fmt.Errorf("invalid schema name %q", newName)
	}
	if schemaFilesMode {
		return fmt.Errorf("ALTER SCHEMA ... RENAME is not supported in multi-file schema mode")
	}
	if !schemaRegistered(ctx, ce, oldName) {
		return fmt.Errorf("schema %q does not exist", oldName)
	}
	if schemaRegistered(ctx, ce, newName) {
		return fmt.Errorf("schema %q already exists", newName)
	}
	tables, err := schemaTables(ctx, ce, oldName)
	if err != nil {
		return err
	}
	for _, t := range tables {
		nt := newName + t[len(oldName):] // swap the "<old>" prefix for "<new>"
		if _, err := ce.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %q RENAME TO %q", t, nt)); err != nil {
			return err
		}
	}
	if _, err := ce.ExecContext(ctx,
		"UPDATE _overlite_schemas SET name = ? WHERE name = ? COLLATE NOCASE", newName, oldName); err != nil {
		return err
	}
	return rebuildCatalog(ctx, ce, mainPath)
}

// setTableSchema moves a table to another schema by renaming it (single-file
// mode). SQLite's RENAME updates foreign keys/views/triggers that reference it.
func setTableSchema(ctx context.Context, ce connExecutor, mainPath, tableRef, newSchema string) error {
	if schemaFilesMode {
		return fmt.Errorf("ALTER TABLE ... SET SCHEMA is not supported in multi-file schema mode")
	}
	srcSchema, name := splitSchemaRef(tableRef)
	if !strings.EqualFold(newSchema, "public") && !schemaRegistered(ctx, ce, newSchema) {
		return fmt.Errorf("schema %q does not exist", newSchema)
	}
	src := storedTableName(srcSchema, name)
	dst := storedTableName(newSchema, name)
	if src == dst {
		return nil
	}
	if _, err := ce.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %q RENAME TO %q", src, dst)); err != nil {
		return err
	}
	return rebuildCatalog(ctx, ce, mainPath)
}

// splitSchemaRef splits "schema.table" into its parts; a bare name is public.
func splitSchemaRef(ref string) (schema, table string) {
	ref = strings.Trim(strings.TrimSpace(ref), `"`)
	if i := strings.IndexByte(ref, '.'); i >= 0 {
		return ref[:i], ref[i+1:]
	}
	return "public", ref
}

// storedTableName is the SQLite table name for a (schema, table): bare in
// public, "<schema>.<table>" otherwise.
func storedTableName(schema, table string) string {
	if schema == "" || strings.EqualFold(schema, "public") {
		return table
	}
	return schema + "." + table
}
