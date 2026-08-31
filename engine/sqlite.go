// Package engine implements core.Engine on top of SQLite using the pure-Go
// modernc.org/sqlite driver (no CGO -> single static binary).
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"overlite/core"

	_ "modernc.org/sqlite"
)

// SQLite is a core.Engine backed by a single SQLite database file (or
// ":memory:"). A single SQLite instance owns the file for the whole process;
// clients reach it over the network via a protocol, which is exactly what
// removes SQLite's network-filesystem locking problem: the file is always
// local to this process and access is serialized through it.
type SQLite struct {
	db       *sql.DB
	conn     *sql.Conn // default connection for the engine's own methods
	mainPath string    // path of the "public" schema file (or ":memory:")
}

// maxConnections caps concurrent client sessions (each pins one connection),
// mirroring PostgreSQL's default max_connections.
const maxConnections = 100

// Open opens (creating if needed) a SQLite database at path. Use ":memory:"
// for an ephemeral in-memory database.
//
// WAL + a busy timeout are enabled so concurrent readers don't block and
// writers wait politely instead of erroring with SQLITE_BUSY. Each client gets
// a dedicated connection (see Session), so reads run in parallel and one
// client's transaction doesn't block the others.
func Open(path string) (*SQLite, error) {
	registerCatalog()
	// Set before the first connection: the catalog is built in a connection
	// hook that reads catalogDBName and schemaFilesMode.
	catalogDBName = dbNameFromPath(path)
	readSchemaMode()
	dsn := buildDSN(path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	db.SetMaxOpenConns(maxConnections)
	// Don't pool idle connections: each new session gets a fresh connection
	// that (re)discovers schemas and rebuilds its catalog on connect, so a
	// reused connection never carries stale ATTACH/temp-view state.
	db.SetMaxIdleConns(0)

	// A dedicated connection for the engine's own methods (used directly in
	// tests and by CREATE/DROP SCHEMA at the engine level).
	conn, err := db.Conn(context.Background())
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	return &SQLite{db: db, conn: conn, mainPath: path}, nil
}

// Session pins a dedicated connection for one client. Implements core.Engine.
func (s *SQLite) Session(ctx context.Context) (core.Session, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	return &sqliteSession{conn: conn, mainPath: s.mainPath}, nil
}

// sqliteSession is one client's dedicated connection.
type sqliteSession struct {
	conn     *sql.Conn
	mainPath string
}

func (ss *sqliteSession) Execute(ctx context.Context, sql string, args []core.Value) (*core.ResultSet, error) {
	rs, err := execute(ctx, ss.conn, sql, args)
	// format_type() renders an enum from a registry loaded when the connection
	// opens. Without this, a type created later in the same session renders as
	// its storage type (text) until the client reconnects.
	if err == nil && strings.Contains(sql, enumTypesTable) {
		refreshEnumNamesFrom(ctx, ss.conn)
	}
	return rs, err
}
func (ss *sqliteSession) Describe(ctx context.Context, sql string, args []core.Value) ([]core.Column, error) {
	return describe(ctx, ss.conn, sql, args)
}
func (ss *sqliteSession) Begin(ctx context.Context) (core.Tx, error) {
	tx, err := ss.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: tx}, nil
}
func (ss *sqliteSession) Close() error { return ss.conn.Close() }

func buildDSN(path string) string {
	// modernc reads pragmas from the DSN query string.
	pragmas := url.Values{}
	pragmas.Add("_pragma", "busy_timeout(5000)")
	pragmas.Add("_pragma", "foreign_keys(on)")
	if path == ":memory:" {
		// A shared-cache in-memory db so every pool connection sees the same
		// data; a plain :memory: would give each connection its own database.
		return "file::memory:?cache=shared&" + pragmas.Encode()
	}
	pragmas.Add("_pragma", "journal_mode(WAL)")
	return "file:" + path + "?" + pragmas.Encode()
}

// querier is satisfied by both *sql.DB (autocommit) and *sql.Tx (transaction),
// so the same execution logic serves both.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// Execute runs a statement on the engine's own connection (used by tests and
// convenience code; clients use Session).
func (s *SQLite) Execute(ctx context.Context, query string, args []core.Value) (*core.ResultSet, error) {
	return execute(ctx, s.conn, query, args)
}

// Begin starts a transaction on the engine's own connection.
func (s *SQLite) Begin(ctx context.Context) (core.Tx, error) {
	tx, err := s.conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &sqliteTx{tx: tx}, nil
}

// sqliteTx runs statements within a database/sql transaction.
type sqliteTx struct{ tx *sql.Tx }

func (t *sqliteTx) Execute(ctx context.Context, sql string, args []core.Value) (*core.ResultSet, error) {
	return execute(ctx, t.tx, sql, args)
}
func (t *sqliteTx) Describe(ctx context.Context, sql string, args []core.Value) ([]core.Column, error) {
	return describe(ctx, t.tx, sql, args)
}
func (t *sqliteTx) Commit() error   { return t.tx.Commit() }
func (t *sqliteTx) Rollback() error { return t.tx.Rollback() }

func execute(ctx context.Context, q querier, query string, args []core.Value) (*core.ResultSet, error) {
	if rs, ok := tryFunctionDDL(ctx, q, query); ok {
		return rs, nil
	}
	query = qualifySchemaNames(query)
	query = resolveSearchPath(ctx, q, query)
	query = rewriteSQLFunctions(query)
	cmd := leadingCommand(query)
	if isQuery(query) {
		return doQuery(ctx, q, query, args, cmd)
	}
	return doExec(ctx, q, query, args, cmd)
}

func doQuery(ctx context.Context, q querier, query string, args []core.Value, cmd string) (*core.ResultSet, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, _ := rows.ColumnTypes()

	cols := make([]core.Column, len(names))
	for i, n := range names {
		cols[i] = core.Column{Name: n, DeclType: columnType(types, i)}
	}

	var out [][]core.Value
	for rows.Next() {
		cells := make([]core.Value, len(names))
		ptrs := make([]any, len(names))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out = append(out, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &core.ResultSet{
		IsQuery:      true,
		Columns:      cols,
		Rows:         out,
		RowsAffected: int64(len(out)),
		Command:      cmd,
	}, nil
}

func doExec(ctx context.Context, q querier, query string, args []core.Value, cmd string) (*core.ResultSet, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return &core.ResultSet{
		IsQuery:      false,
		RowsAffected: affected,
		LastInsertID: lastID,
		Command:      cmd,
	}, nil
}

// Describe implements core.Engine. It returns a query's output columns without
// producing rows and without any side effect. For mutations with RETURNING it
// introspects a read-only projection instead of running the mutation.
func (s *SQLite) Describe(ctx context.Context, query string, args []core.Value) ([]core.Column, error) {
	return describe(ctx, s.conn, query, args)
}

func describe(ctx context.Context, q querier, query string, args []core.Value) ([]core.Column, error) {
	query = qualifySchemaNames(query)
	query = resolveSearchPath(ctx, q, query)
	query = rewriteSQLFunctions(query)
	introSQL, ok := introspectionSQL(query)
	if !ok {
		return nil, nil // statement produces no rows
	}
	// A RETURNING rewrite drops the original placeholders, so it takes no args.
	if introSQL != query {
		args = nil
	}

	rows, err := q.QueryContext(ctx, introSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	types, _ := rows.ColumnTypes()
	cols := make([]core.Column, len(names))
	for i, n := range names {
		cols[i] = core.Column{Name: n, DeclType: columnType(types, i)}
	}
	return cols, nil
}

// columnType returns the best type hint SQLite gives for column i: the declared
// type when the column comes from a table, else the driver's scan type (e.g.
// "int64", "string") for computed columns. Empty for untyped NULL expressions.
func columnType(types []*sql.ColumnType, i int) string {
	if types == nil || i >= len(types) {
		return ""
	}
	if name := types[i].DatabaseTypeName(); name != "" {
		return name
	}
	if st := types[i].ScanType(); st != nil {
		return st.String()
	}
	return ""
}

func (s *SQLite) Close() error {
	s.conn.Close()
	return s.db.Close()
}

// isQuery reports whether a statement is expected to return rows. It is a
// deliberately small heuristic; the protocol layer never has to know SQL.
func isQuery(sql string) bool {
	head := firstWord(sql)
	switch head {
	case "SELECT", "WITH", "PRAGMA", "EXPLAIN", "VALUES", "SHOW":
		return true
	}
	// INSERT/UPDATE/DELETE ... RETURNING also yields rows.
	return strings.Contains(strings.ToUpper(sql), " RETURNING ")
}

// leadingCommand returns the command tag verb, upper-cased. DDL verbs keep
// their second word ("CREATE TABLE") which some protocols report verbatim.
func leadingCommand(sql string) string {
	fields := strings.Fields(strings.ToUpper(strings.TrimSpace(sql)))
	if len(fields) == 0 {
		return ""
	}
	switch fields[0] {
	case "CREATE", "DROP", "ALTER":
		if len(fields) > 1 {
			return fields[0] + " " + fields[1]
		}
	}
	return fields[0]
}

var (
	reInsertReturning = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+("[^"]+"|[\w.]+).*?\bRETURNING\b\s+(.+)$`)
	reUpdateReturning = regexp.MustCompile(`(?is)^\s*UPDATE\s+("[^"]+"|[\w.]+).*?\bRETURNING\b\s+(.+)$`)
	reDeleteReturning = regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+("[^"]+"|[\w.]+).*?\bRETURNING\b\s+(.+)$`)
)

// introspectionSQL returns a read-only statement whose columns match query's
// output, plus ok=false when the statement yields no rows at all.
//
//   - pure reads (SELECT/WITH/VALUES/PRAGMA/EXPLAIN) are returned unchanged;
//   - INSERT/UPDATE/DELETE ... RETURNING become "SELECT <list> FROM <table>
//     WHERE 0", so we learn the column shape without performing the mutation;
//   - everything else (plain DML, DDL) yields ok=false.
func introspectionSQL(sql string) (string, bool) {
	switch firstWord(sql) {
	case "SELECT", "WITH", "VALUES", "PRAGMA", "EXPLAIN":
		return sql, true
	}
	for _, re := range []*regexp.Regexp{reInsertReturning, reUpdateReturning, reDeleteReturning} {
		if m := re.FindStringSubmatch(sql); m != nil {
			return "SELECT " + m[2] + " FROM " + m[1] + " WHERE 0", true
		}
	}
	return "", false
}

// countParams counts positional placeholders in a statement: the highest $N
// index, plus any bare "?" occurrences.
func countParams(sql string) int {
	max, bare := 0, 0
	for i := 0; i < len(sql); i++ {
		switch sql[i] {
		case '?':
			bare++
		case '$':
			j := i + 1
			n := 0
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				n = n*10 + int(sql[j]-'0')
				j++
			}
			if j > i+1 && n > max {
				max = n
			}
			i = j - 1
		}
	}
	if bare > max {
		return bare
	}
	return max
}

func firstWord(sql string) string {
	sql = strings.TrimLeft(sql, " \t\r\n(")
	i := strings.IndexAny(sql, " \t\r\n(")
	if i < 0 {
		i = len(sql)
	}
	return strings.ToUpper(sql[:i])
}
