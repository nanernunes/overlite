package postgres

import "strings"

// DEFAULT nextval('seq') can't be a SQLite column default (non-constant). On
// CREATE TABLE we record the (table, column, sequence) and strip the default;
// on INSERT that omits such a column we inject nextval('seq') (which the
// sequence-expansion pass then turns into the next integer).

// applyColumnDefaults records/strips nextval defaults on CREATE TABLE and injects
// them on INSERT, returning the possibly-modified statement.
func (s *session) applyColumnDefaults(sql string) string {
	switch firstWordUpper(sql) {
	case "CREATE":
		if secondWordUpper(sql) == "TABLE" && strings.Contains(strings.ToLower(sql), "nextval") {
			return s.recordNextvalDefaults(sql)
		}
	case "INSERT":
		if strings.Contains(strings.ToLower(sql), "insert") {
			return s.injectNextvalDefaults(sql)
		}
	}
	return sql
}

// recordNextvalDefaults records each column's nextval default and removes the
// DEFAULT clause from the CREATE TABLE.
func (s *session) recordNextvalDefaults(sql string) string {
	table := createTableName(sql)
	open := strings.IndexByte(sql, '(')
	if table == "" || open < 0 {
		return sql
	}
	inner, after := balancedParen(sql, open)
	defs := splitTopLevel(inner)
	changed := false
	for i, d := range defs {
		name, typ, cons := parseColDef(d)
		if name == "" {
			continue
		}
		seq := nextvalSeq(cons)
		if seq == "" {
			continue
		}
		_, _ = s.exec("INSERT INTO _overlite_defaults (tablename, colname, seqname) VALUES ("+
			sqlStr(table)+", "+sqlStr(unquoteIdent(name))+", "+sqlStr(seq)+")", nil)
		defs[i] = " " + joinDef(name, typ, dropDefault(cons)) + " "
		changed = true
	}
	if !changed {
		return sql
	}
	return sql[:open+1] + strings.Join(defs, ",") + sql[after-1:]
}

// nextvalSeq extracts the sequence name from a `DEFAULT nextval('seq'[::regclass])`
// constraint fragment, or "".
func nextvalSeq(cons string) string {
	low := strings.ToLower(cons)
	n := indexWord(low, "nextval")
	if n < 0 {
		return ""
	}
	op := strings.IndexByte(cons[n:], '(')
	if op < 0 {
		return ""
	}
	inner, _ := balancedParen(cons, n+op)
	inner = strings.TrimSpace(inner)
	// strip a ::regclass cast
	if c := strings.Index(strings.ToLower(inner), "::"); c >= 0 {
		inner = strings.TrimSpace(inner[:c])
	}
	if len(inner) >= 2 && (inner[0] == '\'' || inner[0] == '"') {
		inner = inner[1 : len(inner)-1]
	}
	// a schema-qualified name keeps just the object
	if d := strings.LastIndexByte(inner, '.'); d >= 0 {
		inner = inner[d+1:]
	}
	return inner
}

// createTableName returns the table name of a CREATE TABLE (before its columns).
func createTableName(sql string) string {
	low := strings.ToLower(sql)
	t := indexWord(low, "table")
	if t < 0 {
		return ""
	}
	seg := sql[t+len("table"):]
	if op := strings.IndexByte(seg, '('); op >= 0 {
		seg = seg[:op]
	}
	f := strings.Fields(seg)
	for len(f) > 0 {
		switch strings.ToLower(f[0]) {
		case "if", "not", "exists":
			f = f[1:]
		default:
			return unquoteIdent(f[0])
		}
	}
	return ""
}

// injectNextvalDefaults adds nextval('seq') for any nextval-default column that
// an INSERT's explicit column list omits.
func (s *session) injectNextvalDefaults(sql string) string {
	target, cols, srcStart := parseInsert(sql)
	if target == "" || srcStart < 0 || len(cols) == 0 {
		return sql // no explicit column list -> all columns supplied
	}
	bare := bareTableName(target)
	rs, err := s.exec("SELECT colname, seqname FROM _overlite_defaults WHERE lower(tablename)=lower("+
		sqlStr(bare)+")", nil)
	if err != nil || len(rs.Rows) == 0 {
		return sql
	}
	have := map[string]bool{}
	for _, c := range cols {
		have[strings.ToLower(unquoteIdent(c))] = true
	}
	var addCols, addVals []string
	for _, row := range rs.Rows {
		col := asString(row[0])
		if col == "" || have[strings.ToLower(col)] {
			continue
		}
		addCols = append(addCols, col)
		addVals = append(addVals, "nextval('"+asString(row[1])+"')")
	}
	if len(addCols) == 0 {
		return sql
	}
	source := strings.TrimSpace(sql[srcStart:])
	if !strings.HasPrefix(strings.ToUpper(source), "VALUES") {
		return sql // only the VALUES form is handled
	}
	newValues, ok := injectIntoTuples(sql[srcStart:], addVals)
	if !ok {
		return sql
	}
	newCols := append(append([]string{}, cols...), addCols...)
	return "INSERT INTO " + target + " (" + strings.Join(newCols, ", ") + ") " + newValues
}

// injectIntoTuples appends the given values to each top-level (…) tuple in a
// VALUES clause, leaving any trailing clause (RETURNING …) untouched.
func injectIntoTuples(valuesClause string, add []string) (string, bool) {
	v := indexWord(strings.ToLower(valuesClause), "values")
	if v < 0 {
		return "", false
	}
	head := valuesClause[:v+len("values")]
	rest := valuesClause[v+len("values"):]
	suffix := ""
	if r := topLevelKeyword(rest, "returning"); r >= 0 {
		suffix = " " + rest[r:]
		rest = rest[:r]
	}
	var b strings.Builder
	b.WriteString(head)
	added := ", " + strings.Join(add, ", ")
	i, injected := 0, false
	for i < len(rest) {
		c := rest[i]
		if c == '\'' {
			j := endOfStringLiteral(rest, i)
			b.WriteString(rest[i:j])
			i = j
			continue
		}
		if c == '(' {
			_, e := balancedParen(rest, i)
			b.WriteString(rest[i : e-1]) // the tuple minus its closing ")"
			b.WriteString(added)
			b.WriteByte(')')
			i = e
			injected = true
			continue
		}
		b.WriteByte(c)
		i++
	}
	if !injected {
		return "", false
	}
	return b.String() + suffix, true
}
