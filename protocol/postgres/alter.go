package postgres

import (
	"fmt"
	"regexp"
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
	// pg_dump writes ALTER TABLE ONLY <table>; ONLY is about inheritance,
	// which does not exist here, so it is simply not the table name.
	if i < len(f) && strings.EqualFold(f[i], "only") {
		i++
	}
	if i >= len(f) {
		return "", false, nil
	}
	// Clients qualify with public.; in SQLite that schema is the unqualified
	// name, while any other schema really is part of the table's name. Both
	// halves may be quoted, which is how every ORM writes them.
	table := unquoteRef(rePublic.ReplaceAllString(f[i], ""))
	rest := f[i+1:]
	if len(rest) == 0 {
		return "", false, nil
	}
	switch strings.ToUpper(rest[0]) {
	case "ADD":
		// A rebuild puts the constraint in the CREATE TABLE, where the catalog
		// reads it back as a constraint rather than as a bare index — which is
		// what lets a dump round-trip. Both fallbacks below keep a restore that
		// used to succeed from starting to fail.
		if clause, ok := parseAddConstraint(sql); ok {
			if err := s.alterAddConstraint(table, clause); err == nil {
				return "ALTER TABLE", true, nil
			}
		}
		if hasWord(rest, "unique") && !hasWord(rest, "primary") {
			return "ALTER TABLE", true, s.alterAddUnique(sql, table)
		}
		if def, ok := addColumnComputedDefault(sql); ok {
			// The reference as written, not the unqualified name: the rebuild
			// has to find the table in its own schema.
			return "ALTER TABLE", true, s.alterAddColumnDefault(unquoteRef(f[i]), def)
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
	if i < len(f) && strings.EqualFold(f[i], "only") {
		i++
	}
	rest := f[min(i+1, len(f)):]
	if len(rest) == 0 {
		return false
	}
	switch strings.ToUpper(rest[0]) {
	case "ADD":
		if hasWord(rest, "unique") && !hasWord(rest, "primary") {
			return true
		}
		if _, ok := addColumnComputedDefault(sql); ok {
			return true
		}
		_, ok := parseAddConstraint(sql)
		return ok
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
	return s.rebuildTableCopying(qIdent(table), newDDL, nil, "main.sqlite_master", table)
}

// rebuildTableCopying is rebuildTable for a DDL whose column set differs from
// the current one: copy names the columns to carry over, and any column of the
// new table missing from it takes its default.
//
// table is the qualified spelling to write in statements; master and name say
// where to read the table's indexes and triggers back from.
func (s *session) rebuildTableCopying(table, newDDL string, copy []string, master, name string) error {
	schema, _ := splitQualifiedRef(table)
	aux := qualifyAuxDDL(s.auxDDLIn(master, name), schema)
	tmp := tempRebuildName(table)
	tmpDDL := renameCreateTableTo(newDDL, tmp)

	copyStep := "INSERT INTO " + tmp + " SELECT * FROM " + table
	if len(copy) > 0 {
		quoted := make([]string, len(copy))
		for i, c := range copy {
			quoted[i] = qIdent(c)
		}
		list := strings.Join(quoted, ", ")
		copyStep = "INSERT INTO " + tmp + " (" + list + ") SELECT " + list + " FROM " + table
	}

	if _, err := s.exec("SAVEPOINT _rb", nil); err != nil {
		return err
	}
	// The table disappears for an instant between the DROP and the RENAME, and
	// SQLite checks any foreign key pointing at it as soon as it goes. Deferring
	// enforcement to the end of the savepoint keeps those keys intact without
	// turning them off: PRAGMA foreign_keys itself is ignored inside a
	// transaction, which this is.
	_, _ = s.exec("PRAGMA defer_foreign_keys = ON", nil)
	// RENAME TO re-resolves every reference in the schema so it can fix the
	// ones pointing at the renamed table. Here the table it is replacing has
	// just been dropped, so that pass fails on any foreign key still naming it.
	// The legacy behaviour renames without the fix-up, which is what a
	// create-copy-swap wants: the references already name the final table.
	_, _ = s.exec("PRAGMA legacy_alter_table = ON", nil)
	fail := func(err error) error {
		_, _ = s.exec("ROLLBACK TO _rb", nil)
		_, _ = s.exec("RELEASE _rb", nil)
		_, _ = s.exec("PRAGMA legacy_alter_table = OFF", nil)
		_, _ = s.exec("PRAGMA defer_foreign_keys = OFF", nil)
		return err
	}
	steps := []string{
		tmpDDL,
		copyStep,
		"DROP TABLE " + table,
		// RENAME TO takes a bare name: the table stays where it already is.
		"ALTER TABLE " + tmp + " RENAME TO " + qIdent(name),
	}
	steps = append(steps, aux...)
	for _, st := range steps {
		logQuery("rebuild", st)
		if _, err := s.exec(st, nil); err != nil {
			return fail(err)
		}
	}
	_, _ = s.exec("RELEASE _rb", nil)
	_, _ = s.exec("PRAGMA legacy_alter_table = OFF", nil)
	// Deferral lasts until the enclosing transaction ends, which is not always
	// here: a rebuild inside a client transaction would otherwise leave foreign
	// keys unchecked for the rest of it.
	_, _ = s.exec("PRAGMA defer_foreign_keys = OFF", nil)
	return nil
}

// --- DDL editing ------------------------------------------------------------

func qIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// renameCreateTable replaces the table name in a CREATE TABLE with newName.
func renameCreateTable(ddl, newName string) string {
	return renameCreateTableTo(ddl, qIdent(newName))
}

// renameCreateTableTo is renameCreateTable for a name already rendered as SQL,
// which a schema-qualified one has to be.
func renameCreateTableTo(ddl, rendered string) string {
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
	return ddl[:j] + rendered + ddl[end:]
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

// indexWordFold finds sub in s, case-insensitively, or -1.
func indexWordFold(s, sub string) int {
	return strings.Index(strings.ToUpper(s), strings.ToUpper(sub))
}

// --- ADD CONSTRAINT ---------------------------------------------------------

// parseAddConstraint extracts the table-level constraint clause from an
// ALTER TABLE … ADD [CONSTRAINT name] {PRIMARY KEY|FOREIGN KEY|CHECK} … and
// returns it in the form a CREATE TABLE accepts.
func parseAddConstraint(sql string) (string, bool) {
	body := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	i := indexWordFold(body, " ADD ")
	if i < 0 {
		return "", false
	}
	clause := strings.TrimSpace(body[i+len(" ADD "):])

	// The name is optional; keep it when given so a later dump reads the same.
	name := ""
	if f := strings.Fields(clause); len(f) >= 2 && strings.EqualFold(f[0], "constraint") {
		name = f[1]
		clause = strings.TrimSpace(clause[strings.Index(clause, f[1])+len(f[1]):])
	}

	up := strings.ToUpper(clause)
	switch {
	case strings.HasPrefix(up, "PRIMARY KEY"), strings.HasPrefix(up, "CHECK"),
		strings.HasPrefix(up, "UNIQUE"):
	case strings.HasPrefix(up, "FOREIGN KEY"):
		clause = rePublic.ReplaceAllString(clause, "")
	default:
		return "", false // UNIQUE, EXCLUDE, anything else
	}

	// NOT VALID says "do not check the existing rows", which SQLite has no way
	// to express: the constraint is checked as the rebuild copies them.
	if j := indexWordFold(clause, " NOT VALID"); j >= 0 {
		clause = strings.TrimSpace(clause[:j])
	}
	if name != "" {
		clause = "CONSTRAINT " + name + " " + clause
	}
	return clause, true
}

// alterAddConstraint applies a table-level constraint by rebuilding the table
// with it in the CREATE TABLE, which is the only way SQLite takes one after the
// fact. The rebuild copies the rows, so an existing violation surfaces here as
// an error rather than as a constraint that silently does not hold.
func (s *session) alterAddConstraint(table, clause string) error {
	ddl := s.tableDDL(table)
	if ddl == "" {
		return fmt.Errorf("unknown table %q", table)
	}
	newDDL, ok := insertTableConstraint(ddl, clause)
	if !ok {
		return fmt.Errorf("cannot place constraint in DDL for %q", table)
	}
	return s.rebuildTable(table, newDDL)
}

// insertTableConstraint puts clause at the end of a CREATE TABLE's column list,
// where SQLite reads it as a table constraint. It walks to the paren that
// closes the list rather than the last one in the string, so a DEFAULT (expr)
// or a trailing clause after the list does not mislead it.
func insertTableConstraint(ddl, clause string) (string, bool) {
	open := strings.Index(ddl, "(")
	if open < 0 {
		return "", false
	}
	depth, inStr := 0, byte(0)
	for i := open; i < len(ddl); i++ {
		c := ddl[i]
		if inStr != 0 {
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			inStr = c
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return ddl[:i] + ", " + clause + ddl[i:], true
			}
		}
	}
	return "", false
}

// --- ADD COLUMN with a computed default ---------------------------------------

// SQLite refuses `ALTER TABLE … ADD COLUMN … DEFAULT <expr>` for anything it
// cannot evaluate to a constant, because it would have to compute the value for
// every existing row. Postgres allows it, and migrations lean on it heavily
// (`ADD COLUMN created_at timestamptz DEFAULT now()`), so it is implemented the
// way SQLite's own documentation recommends changing a table: rebuild it with
// the column in the CREATE TABLE, where a computed default is accepted.

// addColumnComputedDefault returns the column definition of an
// `ADD COLUMN … DEFAULT <call>`, and whether the statement is one.
func addColumnComputedDefault(sql string) (def string, ok bool) {
	low := strings.ToLower(sql)
	i := indexWord(low, "add")
	if i < 0 {
		return "", false
	}
	rest := strings.TrimSpace(sql[i+len("add"):])
	if strings.EqualFold(firstWordUpper(rest), "COLUMN") {
		rest = strings.TrimSpace(rest[len("column"):])
	}
	if strings.EqualFold(firstWordUpper(rest), "IF") { // IF NOT EXISTS
		f := strings.Fields(rest)
		if len(f) >= 3 && strings.EqualFold(f[1], "not") && strings.EqualFold(f[2], "exists") {
			rest = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(
				strings.TrimPrefix(strings.TrimSpace(strings.TrimPrefix(rest, f[0])), f[1])), f[2]))
		}
	}
	rest = strings.TrimSuffix(strings.TrimSpace(rest), ";")

	// Only the computed form needs the rebuild; a constant default is fine as
	// a plain ADD COLUMN. This runs on the statement as the client sent it, so
	// the value is still `now()` rather than its SQLite spelling.
	d := indexWord(strings.ToLower(rest), "default")
	if d < 0 {
		return "", false
	}
	if !isComputedDefault(strings.TrimSpace(rest[d+len("default"):])) {
		return "", false
	}
	if name, _, _ := parseColDef(rest); name == "" {
		return "", false
	}
	return rest, true
}

// isComputedDefault reports whether a DEFAULT value is an expression SQLite
// cannot store through ADD COLUMN: a parenthesised expression or a function
// call. A literal or a bare keyword such as CURRENT_TIMESTAMP is constant
// enough for SQLite to accept directly.
func isComputedDefault(value string) bool {
	if strings.HasPrefix(value, "(") {
		return true
	}
	i := 0
	for i < len(value) && isWordByte(value[i]) {
		i++
	}
	if i == 0 {
		return false
	}
	for i < len(value) && value[i] == ' ' {
		i++
	}
	return i < len(value) && value[i] == '('
}

// alterAddColumnDefault adds a column carrying a computed default by rebuilding
// the table with it in place.
func (s *session) alterAddColumnDefault(ref, def string) error {
	name, master, qualified := s.resolveTable(ref)
	ddl := s.tableDDLIn(master, name)
	if ddl == "" {
		return fmt.Errorf("relation %q does not exist", ref)
	}
	open := strings.IndexByte(ddl, '(')
	if open < 0 {
		return fmt.Errorf("cannot read the definition of %q", ref)
	}
	inner, after := balancedParen(ddl, open)

	cols := existingColumnNames(inner)
	if len(cols) == 0 {
		return fmt.Errorf("cannot read the columns of %q", ref)
	}
	// The definition still carries the client's spelling; the rebuild runs it
	// against the engine directly, so it needs the dialect rewrite applied.
	newDDL := ddl[:open+1] + inner + ", " + rewrite(def) + ddl[after-1:]
	return s.rebuildTableCopying(qualified, newDDL, cols, master, name)
}

// existingColumnNames lists the column names in a CREATE TABLE body, skipping
// table-level constraints.
func existingColumnNames(inner string) []string {
	var out []string
	for _, d := range splitTopLevel(inner) {
		name, _, _ := parseColDef(d)
		if name == "" {
			continue
		}
		switch strings.ToUpper(name) {
		case "PRIMARY", "FOREIGN", "UNIQUE", "CHECK", "CONSTRAINT", "EXCLUDE":
			continue
		}
		out = append(out, unquoteIdent(name))
	}
	return out
}

// resolveTable asks the engine how the storage names a table reference. An
// engine without schemas answers for the plain, unqualified case.
func (s *session) resolveTable(ref string) (name, master, qualified string) {
	if sm, ok := s.db.(core.SchemaManager); ok {
		return sm.ResolveTable(ref)
	}
	bare := unquoteIdent(ref)
	return bare, "main.sqlite_master", qIdent(bare)
}

// tempRebuildName places the scratch table beside the one being rebuilt.
//
// It has to land in the same database: in multi-file mode a schema is an
// attached one, and building the replacement in main would leave the rebuilt
// table in public, out of the schema it started in.
func tempRebuildName(qualifiedTable string) string {
	const tmp = "_overlite_rebuild"
	// A dot inside quotes is part of a single stored name (single-file mode),
	// not an attached-database qualifier.
	if schema, _ := splitQualifiedRef(qualifiedTable); schema != "" {
		return qIdent(schema) + "." + qIdent(tmp)
	}
	return qIdent(tmp)
}

// tableDDLIn reads a table's CREATE statement from a given sqlite_master.
func (s *session) tableDDLIn(master, name string) string {
	rs, err := s.exec("SELECT sql FROM "+master+" WHERE type='table' AND lower(name)=lower("+
		sqlStr(name)+")", nil)
	if err != nil || len(rs.Rows) == 0 {
		return ""
	}
	return asString(rs.Rows[0][0])
}

// auxDDLIn reads a table's indexes and triggers from a given sqlite_master.
func (s *session) auxDDLIn(master, name string) []string {
	rs, err := s.exec("SELECT sql FROM "+master+" WHERE lower(tbl_name)=lower("+sqlStr(name)+
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

// unquoteRef strips the quoting from each part of a possibly qualified table
// reference, leaving `schema.table` or `table`.
func unquoteRef(ref string) string {
	schema, table := splitQualifiedRef(ref)
	if schema == "" {
		return table
	}
	return schema + "." + table
}

// splitQualifiedRef splits `"schema"."table"` (in any combination of quoting)
// into its unquoted parts.
func splitQualifiedRef(ref string) (schema, table string) {
	depth := 0
	for i := 0; i < len(ref); i++ {
		switch ref[i] {
		case '"':
			depth ^= 1
		case '.':
			if depth == 0 {
				return unquoteIdent(ref[:i]), unquoteIdent(ref[i+1:])
			}
		}
	}
	return "", unquoteIdent(ref)
}

// reAuxObjectName captures the name of an index or trigger in its CREATE.
var reAuxObjectName = regexp.MustCompile(
	`(?is)^(\s*CREATE\s+(?:UNIQUE\s+)?(?:INDEX|TRIGGER)\s+(?:IF\s+NOT\s+EXISTS\s+)?)("[^"]+"|[\w.]+)`)

// qualifyAuxDDL puts an index or trigger back in the schema it came from.
//
// A schema is an attached database in multi-file mode, and SQLite names those
// objects by qualifying the object rather than the table. sqlite_master stores
// the statement without that qualifier, so replaying it verbatim would rebuild
// the index in main, against a table that is not there.
func qualifyAuxDDL(stmts []string, schema string) []string {
	if schema == "" {
		return stmts
	}
	out := make([]string, 0, len(stmts))
	for _, stmt := range stmts {
		m := reAuxObjectName.FindStringSubmatch(stmt)
		if m == nil {
			out = append(out, stmt)
			continue
		}
		name := unquoteIdent(m[2])
		out = append(out, m[1]+qIdent(schema)+"."+qIdent(name)+stmt[len(m[0]):])
	}
	return out
}
