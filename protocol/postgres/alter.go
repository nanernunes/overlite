package postgres

import (
	"fmt"
	"strings"

	"overlite/core"
)

// ALTER TABLE forms SQLite can't run natively. UNIQUE constraints become a real
// unique index; column-definition changes (TYPE / NOT NULL / DEFAULT) are done
// with SQLite's recommended table rebuild (create-copy-swap) inside a savepoint,
// preserving data and recreating indexes/triggers. PRIMARY KEY / FOREIGN KEY /
// CHECK ADD CONSTRAINTs stay accepted-but-not-enforced (pg_dump restore relies
// on that), handled by the existing no-op path.

// tryAlterTable handles the ALTER TABLE forms we can implement, returning
// handled=false for the rest (which fall through to the no-op interceptor).
func (s *session) tryAlterTable(sql string) (string, bool, error) {
	if !strings.EqualFold(firstWordUpper(sql), "ALTER") || secondWordUpper(sql) != "TABLE" {
		return "", false, nil
	}
	f := strings.Fields(sql)
	i := 2
	if len(f) > 3 && strings.EqualFold(f[2], "if") && strings.EqualFold(f[3], "exists") {
		i = 4
	}
	if i >= len(f) {
		return "", false, nil
	}
	table := unquoteIdent(f[i])
	rest := f[i+1:]
	if len(rest) == 0 {
		return "", false, nil
	}
	switch strings.ToUpper(rest[0]) {
	case "ADD":
		if hasWord(rest, "unique") && !hasWord(rest, "primary") {
			return "ALTER TABLE", true, s.alterAddUnique(sql, table)
		}
	case "ALTER":
		return "ALTER TABLE", true, s.alterColumn(sql, table, rest[1:])
	case "SET":
		// ALTER TABLE t SET SCHEMA y — move the table between schemas.
		if len(rest) >= 3 && strings.EqualFold(rest[1], "schema") {
			return "ALTER TABLE", true, s.alterSetSchema(unquoteIdent(f[i]), unquoteIdent(rest[2]))
		}
	}
	return "", false, nil
}

// alterSetSchema moves a table to another schema via the engine's SchemaManager.
func (s *session) alterSetSchema(tableRef, newSchema string) error {
	sm, ok := s.db.(core.SchemaManager)
	if !ok {
		return fmt.Errorf("schemas are not supported")
	}
	return sm.SetTableSchema(s.ctx, tableRef, newSchema)
}

// alterTableHandled reports whether tryAlterTable would take over this ALTER
// (so the extended path can route it there instead of the no-op interceptor).
func alterTableHandled(sql string) bool {
	if !strings.EqualFold(firstWordUpper(sql), "ALTER") || secondWordUpper(sql) != "TABLE" {
		return false
	}
	f := strings.Fields(sql)
	i := 2
	if len(f) > 3 && strings.EqualFold(f[2], "if") && strings.EqualFold(f[3], "exists") {
		i = 4
	}
	rest := f[min(i+1, len(f)):]
	if len(rest) == 0 {
		return false
	}
	switch strings.ToUpper(rest[0]) {
	case "ADD":
		return hasWord(rest, "unique") && !hasWord(rest, "primary")
	case "ALTER":
		return true
	case "SET":
		return len(rest) >= 3 && strings.EqualFold(rest[1], "schema")
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func hasWord(toks []string, w string) bool {
	for _, t := range toks {
		if strings.EqualFold(strings.TrimRight(t, "(),;"), w) {
			return true
		}
	}
	return false
}

// alterAddUnique turns ADD [CONSTRAINT name] UNIQUE (cols) into a unique index.
func (s *session) alterAddUnique(sql, table string) error {
	name, cols, ok := parseAddUnique(sql)
	if !ok {
		return fmt.Errorf("syntax error in ADD UNIQUE")
	}
	if name == "" {
		name = table + "_" + strings.ReplaceAll(cols, " ", "") + "_key"
	}
	_, err := s.exec("CREATE UNIQUE INDEX "+qIdent(name)+" ON "+qIdent(table)+" ("+cols+")", nil)
	return err
}

// parseAddUnique extracts the optional constraint name and the column list.
func parseAddUnique(sql string) (name, cols string, ok bool) {
	low := strings.ToLower(sql)
	if c := indexWord(low, "constraint"); c >= 0 {
		after := strings.Fields(sql[c+len("constraint"):])
		if len(after) > 0 {
			name = unquoteIdent(after[0])
		}
	}
	u := indexWord(low, "unique")
	if u < 0 {
		return "", "", false
	}
	op := strings.IndexByte(sql[u:], '(')
	if op < 0 {
		return "", "", false
	}
	inner, _ := balancedParen(sql, u+op)
	return name, strings.TrimSpace(inner), true
}

// alterColumn handles ALTER [COLUMN] c { TYPE t | SET/DROP NOT NULL | SET/DROP
// DEFAULT } via a table rebuild.
func (s *session) alterColumn(sql, table string, rest []string) error {
	if len(rest) > 0 && strings.EqualFold(rest[0], "column") {
		rest = rest[1:]
	}
	if len(rest) < 2 {
		return fmt.Errorf("syntax error in ALTER COLUMN")
	}
	col := unquoteIdent(rest[0])
	rest = rest[1:]
	up := func(i int) string {
		if i < len(rest) {
			return strings.ToUpper(rest[i])
		}
		return ""
	}

	var edit func(name, typ, cons string) string
	switch {
	case up(0) == "TYPE" || (up(0) == "SET" && up(1) == "DATA" && up(2) == "TYPE"):
		newType := columnTypeArg(sql)
		if newType == "" {
			return fmt.Errorf("syntax error in ALTER COLUMN TYPE")
		}
		newType = reNumericType.ReplaceAllString(newType, "DECIMALTEXT COLLATE DECIMAL")
		edit = func(name, _, cons string) string { return joinDef(name, newType, cons) }
	case up(0) == "SET" && up(1) == "NOT" && up(2) == "NULL":
		edit = func(name, typ, cons string) string { return joinDef(name, typ, addNotNull(cons)) }
	case up(0) == "DROP" && up(1) == "NOT" && up(2) == "NULL":
		edit = func(name, typ, cons string) string { return joinDef(name, typ, dropNotNull(cons)) }
	case up(0) == "SET" && up(1) == "DEFAULT":
		def := defaultArg(sql)
		edit = func(name, typ, cons string) string { return joinDef(name, typ, setDefault(cons, def)) }
	case up(0) == "DROP" && up(1) == "DEFAULT":
		edit = func(name, typ, cons string) string { return joinDef(name, typ, dropDefault(cons)) }
	default:
		return fmt.Errorf("unsupported ALTER COLUMN action")
	}

	ddl := s.tableDDL(table)
	if ddl == "" {
		return fmt.Errorf("relation %q does not exist", table)
	}
	newDDL, ok := editColumn(ddl, col, edit)
	if !ok {
		return fmt.Errorf("column %q of relation %q does not exist", col, table)
	}
	return s.rebuildTable(table, newDDL)
}

// columnTypeArg returns the type after "TYPE" in an ALTER COLUMN … TYPE, up to an
// optional USING clause.
func columnTypeArg(sql string) string {
	low := strings.ToLower(sql)
	t := indexWord(low, "type")
	if t < 0 {
		return ""
	}
	seg := strings.TrimSpace(sql[t+len("type"):])
	if u := indexWord(strings.ToLower(seg), "using"); u >= 0 {
		seg = seg[:u]
	}
	return strings.TrimRight(strings.TrimSpace(seg), ";")
}

func defaultArg(sql string) string {
	low := strings.ToLower(sql)
	d := strings.Index(low, "set default")
	if d < 0 {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(sql[d+len("set default"):]), ";")
}

// --- rebuild ----------------------------------------------------------------

func (s *session) tableDDL(table string) string {
	rs, err := s.exec("SELECT sql FROM sqlite_master WHERE type='table' AND lower(name)=lower("+
		sqlStr(table)+")", nil)
	if err != nil || len(rs.Rows) == 0 {
		return ""
	}
	return asString(rs.Rows[0][0])
}

func (s *session) auxDDL(table string) []string {
	rs, err := s.exec("SELECT sql FROM sqlite_master WHERE lower(tbl_name)=lower("+sqlStr(table)+
		") AND type IN ('index','trigger') AND sql IS NOT NULL", nil)
	if err != nil {
		return nil
	}
	var out []string
	for _, row := range rs.Rows {
		if d := asString(row[0]); d != "" {
			out = append(out, d)
		}
	}
	return out
}

// rebuildTable replaces table with newDDL (same column set) via create-copy-swap
// inside a savepoint, recreating its indexes and triggers.
func (s *session) rebuildTable(table, newDDL string) error {
	aux := s.auxDDL(table)
	const tmp = "_overlite_rebuild"
	tmpDDL := renameCreateTable(newDDL, tmp)

	if _, err := s.exec("SAVEPOINT _rb", nil); err != nil {
		return err
	}
	fail := func(err error) error {
		_, _ = s.exec("ROLLBACK TO _rb", nil)
		_, _ = s.exec("RELEASE _rb", nil)
		return err
	}
	steps := []string{
		tmpDDL,
		"INSERT INTO " + qIdent(tmp) + " SELECT * FROM " + qIdent(table),
		"DROP TABLE " + qIdent(table),
		"ALTER TABLE " + qIdent(tmp) + " RENAME TO " + qIdent(table),
	}
	steps = append(steps, aux...)
	for _, st := range steps {
		if _, err := s.exec(st, nil); err != nil {
			return fail(err)
		}
	}
	_, _ = s.exec("RELEASE _rb", nil)
	return nil
}

// --- DDL editing ------------------------------------------------------------

func qIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// renameCreateTable replaces the table name in a CREATE TABLE with newName.
func renameCreateTable(ddl, newName string) string {
	low := strings.ToLower(ddl)
	i := indexWord(low, "table")
	if i < 0 {
		return ddl
	}
	j := i + len("table")
	// skip IF NOT EXISTS
	if k := indexWord(low[j:], "if"); k >= 0 && k < 6 {
		if p := indexWord(low, "exists"); p > j {
			j = p + len("exists")
		}
	}
	for j < len(ddl) && (ddl[j] == ' ' || ddl[j] == '\t' || ddl[j] == '\n') {
		j++
	}
	end := j
	if end < len(ddl) && ddl[end] == '"' {
		end++
		for end < len(ddl) && ddl[end] != '"' {
			end++
		}
		if end < len(ddl) {
			end++
		}
	} else {
		for end < len(ddl) && (isIdentPart(ddl[end]) || ddl[end] == '.') {
			end++
		}
	}
	return ddl[:j] + qIdent(newName) + ddl[end:]
}

// editColumn finds the definition of col in a CREATE TABLE's column list and
// rewrites it with edit(name, type, constraints).
func editColumn(ddl, col string, edit func(name, typ, cons string) string) (string, bool) {
	open := strings.IndexByte(ddl, '(')
	if open < 0 {
		return "", false
	}
	inner, after := balancedParen(ddl, open)
	defs := splitTopLevel(inner)
	found := false
	for idx, d := range defs {
		name, typ, cons := parseColDef(d)
		if name != "" && strings.EqualFold(unquoteIdent(name), col) {
			defs[idx] = " " + edit(name, typ, cons) + " "
			found = true
			break
		}
	}
	if !found {
		return "", false
	}
	return ddl[:open+1] + strings.Join(defs, ",") + ddl[after-1:], true
}

// parseColDef splits a column definition into its name, type, and the remaining
// constraint text.
func parseColDef(def string) (name, typ, cons string) {
	d := strings.TrimSpace(def)
	if d == "" {
		return "", "", ""
	}
	// A table-level constraint isn't a column def.
	first := strings.ToUpper(strings.TrimLeft(d, `"`))
	for _, kw := range []string{"CONSTRAINT ", "PRIMARY ", "UNIQUE ", "CHECK", "FOREIGN ", "EXCLUDE "} {
		if strings.HasPrefix(first, kw) {
			return "", "", ""
		}
	}
	// name
	var np int
	if d[0] == '"' {
		np = 1
		for np < len(d) && d[np] != '"' {
			np++
		}
		if np < len(d) {
			np++
		}
	} else {
		for np < len(d) && (isIdentPart(d[np]) || d[np] == '.') {
			np++
		}
	}
	name = d[:np]
	rest := strings.TrimSpace(d[np:])
	// type = tokens up to the first column-constraint keyword (paren-aware)
	stop := map[string]bool{"not": true, "null": true, "default": true, "primary": true,
		"unique": true, "check": true, "references": true, "collate": true,
		"generated": true, "as": true, "constraint": true}
	depth, k := 0, 0
	for k < len(rest) {
		if rest[k] == '(' {
			depth++
			k++
			continue
		}
		if rest[k] == ')' {
			depth--
			k++
			continue
		}
		if depth == 0 && isIdentStart(rest[k]) {
			w := k
			for w < len(rest) && isIdentPart(rest[w]) {
				w++
			}
			if stop[strings.ToLower(rest[k:w])] {
				break
			}
			k = w
			continue
		}
		k++
	}
	return name, strings.TrimSpace(rest[:k]), strings.TrimSpace(rest[k:])
}

func joinDef(name, typ, cons string) string {
	out := name
	if typ != "" {
		out += " " + typ
	}
	if cons != "" {
		out += " " + cons
	}
	return out
}

func addNotNull(cons string) string {
	if indexWord(strings.ToLower(cons), "not") >= 0 && indexWord(strings.ToLower(cons), "null") >= 0 {
		return cons
	}
	if cons == "" {
		return "NOT NULL"
	}
	return cons + " NOT NULL"
}

func dropNotNull(cons string) string {
	low := strings.ToLower(cons)
	if i := strings.Index(low, "not null"); i >= 0 {
		return strings.TrimSpace(cons[:i] + cons[i+len("not null"):])
	}
	return cons
}

func setDefault(cons, def string) string {
	cons = dropDefault(cons)
	if cons == "" {
		return "DEFAULT " + def
	}
	return cons + " DEFAULT " + def
}

// dropDefault removes a DEFAULT clause (value or parenthesized expression).
func dropDefault(cons string) string {
	low := strings.ToLower(cons)
	i := indexWord(low, "default")
	if i < 0 {
		return cons
	}
	j := i + len("default")
	for j < len(cons) && cons[j] == ' ' {
		j++
	}
	if j < len(cons) && cons[j] == '(' {
		_, e := balancedParen(cons, j)
		j = e
	} else if j < len(cons) && cons[j] == '\'' {
		j = endOfStringLiteral(cons, j)
	} else {
		for j < len(cons) && cons[j] != ' ' {
			j++
		}
	}
	return strings.TrimSpace(cons[:i] + cons[j:])
}
