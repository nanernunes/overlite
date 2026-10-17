package postgres

import (
	"regexp"
	"strconv"
	"strings"
)

/*
Parameter types, for Describe: Postgres says what each $n is, and clients
encode their values by it. SQLite knows no such thing, so every parameter
went out unspecified, and a client sent a boolean as the text "t" — stored as
that string, not SQLite's 1, never equal to true again.

A parameter put straight into a column — INSERT's VALUES, `col = $n` in an
UPDATE, a DELETE or a one-table SELECT — takes that column's type, from the
table's declaration. Only booleans are advertised: the one type whose text
form SQLite cannot take for what it is; the rest stay as they were.
*/

const table = `((?:"[^"]+"|\w+)(?:\s*\.\s*(?:"[^"]+"|\w+))?)`

var (
	insertInto  = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+` + table + `\s*\(([^)]*)\)\s*VALUES\s*`)
	updateTable = regexp.MustCompile(`(?is)^\s*UPDATE\s+(?:ONLY\s+)?` + table + `\s+(?:AS\s+\w+\s+)?SET\s`)
	deleteFrom  = regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+(?:ONLY\s+)?` + table + `(?:\s+(?:AS\s+)?\w+)?\s+WHERE\s`)
	selectFrom  = regexp.MustCompile(`(?is)^\s*SELECT\s.*?\sFROM\s+` + table + `(?:\s+(?:AS\s+)?\w+)?\s+WHERE\s`)
	compared    = regexp.MustCompile(`(?i)(?:\w+\s*\.\s*)?("[^"]+"|\b\w+)\s*=\s*\$(\d+)\b`)
	placeholder = regexp.MustCompile(`^\$(\d+)$`)
)

// paramTypes is the OID of each of a statement's n parameters, 0 for unknown.
func (s *session) paramTypes(sql string, n int) []uint32 {
	oids := make([]uint32, n)
	if n == 0 {
		return oids
	}
	tbl, cols := paramColumns(sql)
	if tbl == "" || len(cols) == 0 {
		return oids
	}
	bools := s.boolColumns(tbl)
	for i, col := range cols {
		if i >= 1 && i <= n && bools[strings.ToLower(col)] {
			oids[i-1] = oidBool
		}
	}
	return oids
}

// paramColumns is the table a statement writes or reads, and the column each
// parameter goes into, by its number.
func paramColumns(sql string) (string, map[int]string) {
	cols := map[int]string{}
	if m := insertInto.FindStringSubmatchIndex(sql); m != nil {
		tbl := sql[m[2]:m[3]]
		names := splitTop(sql[m[4]:m[5]])
		for _, tuple := range tuples(sql[m[1]:]) {
			for i, item := range splitTop(tuple) {
				if p := placeholder.FindStringSubmatch(strings.TrimSpace(item)); p != nil && i < len(names) {
					n, _ := strconv.Atoi(p[1])
					cols[n] = unquote(names[i])
				}
			}
		}
		return tbl, cols
	}
	var tbl string
	for _, re := range []*regexp.Regexp{updateTable, deleteFrom, selectFrom} {
		if m := re.FindStringSubmatch(sql); m != nil {
			tbl = m[1]
			break
		}
	}
	if tbl == "" || strings.Contains(strings.ToUpper(sql), " JOIN ") {
		return "", nil
	}
	for _, m := range compared.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[2])
		cols[n] = unquote(m[1])
	}
	return tbl, cols
}

// boolColumns is a table's columns declared boolean, by lowercase name.
func (s *session) boolColumns(tbl string) map[string]bool {
	parts := strings.Split(tbl, ".")
	name := unquote(parts[len(parts)-1])
	if len(parts) == 2 && !strings.EqualFold(unquote(parts[0]), "public") {
		return nil
	}
	rs, err := s.exec("SELECT name, type FROM pragma_table_info("+sqlStr(name)+")", nil)
	if err != nil || rs == nil {
		return nil
	}
	out := map[string]bool{}
	for _, row := range rs.Rows {
		if len(row) == 2 && strings.Contains(strings.ToUpper(asString(row[1])), "BOOL") {
			out[strings.ToLower(asString(row[0]))] = true
		}
	}
	return out
}

func unquote(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

// tuples is the parenthesised rows a VALUES list starts with: their insides.
func tuples(s string) []string {
	var out []string
	for {
		s = strings.TrimLeft(s, " \t\r\n,")
		if !strings.HasPrefix(s, "(") {
			return out
		}
		end := closing(s)
		if end < 0 {
			return out
		}
		out = append(out, s[1:end])
		s = s[end+1:]
	}
}

// closing is the index of the parenthesis closing the one s starts with.
func closing(s string) int {
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits on the commas outside parentheses and quotes.
func splitTop(s string) []string {
	var out []string
	depth, quoted, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
