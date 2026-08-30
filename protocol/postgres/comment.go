package postgres

import "strings"

// COMMENT ON <objtype> <name> IS <'text'|NULL> records an object comment in
// _overlite_comments (SQLite has nowhere native to keep it). pg_description
// reads that table, and obj_description()/col_description() are rewritten to
// look it up, so psql's \d+ shows table and column comments.

// isComment reports whether sql is a COMMENT ON statement.
func isComment(sql string) bool {
	f := strings.Fields(sql)
	return len(f) >= 2 && strings.EqualFold(f[0], "comment") && strings.EqualFold(f[1], "on")
}

// tryComment handles COMMENT ON. It returns (tag, true, err) once it recognizes
// the statement; a parse it can't model is accepted as a no-op so dumps proceed.
func (s *session) tryComment(sql string) (tag string, handled bool, err error) {
	if !isComment(sql) {
		return "", false, nil
	}
	isPos := commentIsPos(sql)
	if isPos < 0 {
		return "COMMENT", true, nil // not the form we model; accept as no-op
	}
	head := strings.Fields(sql[:isPos])
	value := strings.TrimSpace(strings.TrimRight(sql[isPos+3:], "; \t\n"))
	if len(head) < 4 {
		return "COMMENT", true, nil
	}
	// Object type may be two words (MATERIALIZED VIEW, FOREIGN TABLE); collapse
	// to the relation kind and take the name from what's left.
	objkind := strings.ToLower(head[2])
	rest := head[3:]
	if (objkind == "materialized" || objkind == "foreign") && len(rest) >= 2 {
		objkind = strings.ToLower(rest[0]) // view / table
		rest = rest[1:]
	}

	var objname, subname string
	if objkind == "column" {
		parts := splitDotIdent(strings.Join(rest, ""))
		if len(parts) < 2 {
			return "COMMENT", true, nil
		}
		objname = parts[len(parts)-2] // table (drop any schema before it)
		subname = parts[len(parts)-1] // column
	} else {
		objname = commentIdent(rest[0])
	}

	// Replace any existing comment on the object (COMMENT is idempotent).
	del := "DELETE FROM _overlite_comments WHERE objkind = " + sqlStr(objkind) +
		" AND objname = " + sqlStr(objname) + " AND subname = " + sqlStr(subname)
	if _, err := s.exec(del, nil); err != nil {
		return "COMMENT", true, err
	}
	if strings.EqualFold(value, "null") {
		return "COMMENT", true, nil // IS NULL removes the comment
	}
	text := unquoteSQLString(value)
	ins := "INSERT INTO _overlite_comments (objkind, objname, subname, comment) VALUES (" +
		sqlStr(objkind) + ", " + sqlStr(objname) + ", " + sqlStr(subname) + ", " + sqlStr(text) + ")"
	_, err = s.exec(ins, nil)
	return "COMMENT", true, err
}

// isSpace and helpers below.

// commentIsPos returns the index of the whitespace preceding the top-level IS
// keyword, or -1. It skips single-quoted strings so an IS inside the comment
// text isn't mistaken for the separator.
func commentIsPos(sql string) int {
	for i := 0; i < len(sql); i++ {
		if sql[i] == '\'' {
			i = skipString(sql, i) - 1
			continue
		}
		if isSpace(sql[i]) && i+3 < len(sql) &&
			(sql[i+1] == 'i' || sql[i+1] == 'I') && (sql[i+2] == 's' || sql[i+2] == 'S') &&
			isSpace(sql[i+3]) {
			return i
		}
	}
	return -1
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// commentIdent strips a schema qualifier, surrounding double quotes and a
// trailing cast from an object name.
func commentIdent(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '.'); i >= 0 && !strings.Contains(s, `"`) {
		s = s[i+1:]
	}
	return strings.Trim(s, `"`)
}

// splitDotIdent splits a dotted name on '.' outside double quotes and unquotes
// each part (schema.table.column -> [schema, table, column]).
func splitDotIdent(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '"':
			inQuote = !inQuote
		case s[i] == '.' && !inQuote:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteByte(s[i])
		}
	}
	out = append(out, b.String())
	return out
}

// unquoteSQLString turns a single-quoted SQL literal into its text value
// (dropping the quotes and collapsing doubled single quotes). A bare word is
// returned as-is.
func unquoteSQLString(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '\'' && s[len(s)-1] == '\'' {
		return strings.ReplaceAll(s[1:len(s)-1], "''", "'")
	}
	return s
}
