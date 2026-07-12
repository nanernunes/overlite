package engine

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"overlite/core"
)

// LANGUAGE sql functions are executed by inlining: a call f(a, b) is rewritten
// into the function's body with the arguments substituted, wrapped in
// parentheses — a scalar subquery in an expression position, or a derived table
// in a FROM. This needs no nested execution (which SQLite can't do from a
// callback). PL/pgSQL and other languages are still accepted as a no-op.
//
// Definitions live in `_overlite_functions` (persistent, hidden) and are cached
// in memory for the rewrite; the cache is reloaded on CREATE/DROP FUNCTION and
// per connection.

const functionsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_functions (
  name    TEXT COLLATE NOCASE,
  arity   INTEGER,
  params  TEXT,
  body    TEXT,
  lang    TEXT,
  args    TEXT DEFAULT '',
  rettype TEXT DEFAULT '',
  src     TEXT DEFAULT '',
  PRIMARY KEY (name, arity)
)`

// functionsAddColsDDL migrate older tables (each errors harmlessly if present).
var functionsAddColsDDL = []string{
	`ALTER TABLE _overlite_functions ADD COLUMN args TEXT DEFAULT ''`,
	`ALTER TABLE _overlite_functions ADD COLUMN rettype TEXT DEFAULT ''`,
	`ALTER TABLE _overlite_functions ADD COLUMN src TEXT DEFAULT ''`,
}

type sqlFunc struct {
	oid     int64 // rowid + funcOIDBase; how pg_proc/pg_get_functiondef key it
	name    string
	arity   int
	params  []string // parameter names ("" for an unnamed slot), len == arity
	body    string   // the SQL body, e.g. "SELECT price * (1 + rate)"
	lang    string
	args    string // raw param list, e.g. "price numeric, rate numeric"
	rettype string // e.g. "numeric" or "TABLE(id int, sal int)"
	src     string // the original CREATE FUNCTION statement
}

// funcOIDBase offsets a function's rowid into its catalog oid. It stays below
// pg_dump's built-in cutoff considerations while clearing the pg_catalog range.
const funcOIDBase = 500000

var (
	funcCacheMu sync.RWMutex
	funcCache   = map[string]sqlFunc{} // key: lower(name)+"/"+arity
	funcNames   = map[string]bool{}    // lower(name) present, for a quick scan guard
	funcByOid   = map[int64]sqlFunc{}  // oid -> def, for pg_get_function*
)

// funcByOidLookup returns the definition for a user-function oid.
func funcByOidLookup(oid int64) (sqlFunc, bool) {
	funcCacheMu.RLock()
	defer funcCacheMu.RUnlock()
	f, ok := funcByOid[oid]
	return f, ok
}

func funcKey(name string, arity int) string {
	return strings.ToLower(name) + "/" + strconv.Itoa(arity)
}

func setFunctionCache(fns []sqlFunc) {
	m := make(map[string]sqlFunc, len(fns))
	names := make(map[string]bool, len(fns))
	byOid := make(map[int64]sqlFunc, len(fns))
	for _, f := range fns {
		m[funcKey(f.name, f.arity)] = f
		names[strings.ToLower(f.name)] = true
		if f.oid != 0 {
			byOid[f.oid] = f
		}
	}
	funcCacheMu.Lock()
	funcCache, funcNames, funcByOid = m, names, byOid
	funcCacheMu.Unlock()
}

func lookupFunc(name string, arity int) (sqlFunc, bool) {
	funcCacheMu.RLock()
	defer funcCacheMu.RUnlock()
	f, ok := funcCache[funcKey(name, arity)]
	return f, ok
}

func anyFuncNamed(name string) bool {
	funcCacheMu.RLock()
	defer funcCacheMu.RUnlock()
	return funcNames[strings.ToLower(name)]
}

func haveFunctions() bool {
	funcCacheMu.RLock()
	defer funcCacheMu.RUnlock()
	return len(funcCache) > 0
}

// reloadFunctions rebuilds the cache from _overlite_functions.
func reloadFunctions(ctx context.Context, q querier) {
	rows, err := q.QueryContext(ctx,
		"SELECT rowid, name, arity, params, body, lang, args, rettype, src FROM _overlite_functions")
	if err != nil {
		return
	}
	defer rows.Close()
	var fns []sqlFunc
	for rows.Next() {
		var f sqlFunc
		var rowid int64
		var params string
		if err := rows.Scan(&rowid, &f.name, &f.arity, &params, &f.body, &f.lang, &f.args, &f.rettype, &f.src); err != nil {
			return
		}
		f.oid = rowid + funcOIDBase
		if params != "" {
			f.params = strings.Split(params, "\x1f")
		} else {
			f.params = make([]string, f.arity)
		}
		if strings.EqualFold(f.lang, "sql") {
			fns = append(fns, f)
		}
	}
	setFunctionCache(fns)
}

// refreshFunctionsFromRows rebuilds the cache from rows shaped
// "name<US>arity<US>params<US>body<US>lang" joined with char(30) between
// columns (params itself uses char(31) between names). Used by setupConnection,
// which only has a single-column query helper.
func refreshFunctionsFromRows(rows []string) {
	var fns []sqlFunc
	for _, r := range rows {
		c := strings.Split(r, "\x1e")
		if len(c) < 9 || !strings.EqualFold(c[5], "sql") {
			continue
		}
		f := sqlFunc{
			oid: int64(atoiSafe(c[0])) + funcOIDBase, name: c[1], arity: atoiSafe(c[2]),
			body: c[4], lang: c[5], args: c[6], rettype: c[7], src: c[8],
		}
		if c[3] != "" {
			f.params = strings.Split(c[3], "\x1f")
		} else {
			f.params = make([]string, f.arity)
		}
		fns = append(fns, f)
	}
	setFunctionCache(fns)
}

// tryFunctionDDL handles CREATE/DROP FUNCTION. A LANGUAGE sql function is stored
// and cached; any other language is accepted without storing (a no-op).
func tryFunctionDDL(ctx context.Context, q querier, sql string) (*core.ResultSet, bool) {
	w := leadingWord(sql)
	switch {
	case strings.EqualFold(w, "create") && hasFunctionKeyword(sql):
		fn, ok := parseCreateFunction(sql)
		if ok && strings.EqualFold(fn.lang, "sql") && fn.name != "" {
			params := strings.Join(fn.params, "\x1f")
			_, err := q.ExecContext(ctx,
				"INSERT OR REPLACE INTO _overlite_functions (name, arity, params, body, lang, args, rettype, src)"+
					" VALUES (?,?,?,?,?,?,?,?)",
				fn.name, fn.arity, params, fn.body, "sql", fn.args, fn.rettype, strings.TrimSpace(sql))
			if err == nil {
				reloadFunctions(ctx, q)
			}
		}
		return &core.ResultSet{Command: "CREATE FUNCTION"}, true
	case strings.EqualFold(w, "drop") && hasFunctionKeyword(sql):
		if name, arity, ok := parseDropFunction(sql); ok {
			if arity >= 0 {
				_, _ = q.ExecContext(ctx, "DELETE FROM _overlite_functions WHERE name = ? AND arity = ?", name, arity)
			} else {
				_, _ = q.ExecContext(ctx, "DELETE FROM _overlite_functions WHERE name = ?", name)
			}
			reloadFunctions(ctx, q)
		}
		return &core.ResultSet{Command: "DROP FUNCTION"}, true
	}
	return nil, false
}

func hasFunctionKeyword(sql string) bool {
	f := strings.Fields(strings.ToLower(sql))
	for i := 0; i < len(f) && i < 4; i++ {
		if f[i] == "function" {
			return true
		}
	}
	return false
}

func leadingWord(sql string) string {
	sql = strings.TrimLeft(sql, " \t\n\r(")
	i := 0
	for i < len(sql) && !(sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n' || sql[i] == '\r') {
		i++
	}
	return sql[:i]
}
