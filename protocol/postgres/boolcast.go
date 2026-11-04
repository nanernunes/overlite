package postgres

import "strings"

/*
A cast to boolean is pg_bool(x), the engine's boolin. SQLite's CAST(x AS
boolean) gives the value NUMERIC affinity, which reads every word as 0:
'true'::boolean was false, without an error, and a parameter bound as the
text "true" and cast to boolean was stored as false.

Only in statements that run now. A DDL's default or check keeps SQLite's own
CAST, so the database file still opens without overlite's functions.
*/

// rewriteBoolCasts turns CAST(x AS bool) and CAST(x AS boolean) into
// pg_bool(x). It runs after rewriteCasts, which writes x::boolean as a CAST.
func rewriteBoolCasts(sql string) string {
	if h := firstWordUpper(sql); h == "CREATE" || h == "ALTER" {
		return sql
	}
	if !strings.Contains(strings.ToUpper(sql), "BOOL") {
		return sql
	}
	var b strings.Builder
	quoted := false
	for i := 0; i < len(sql); i++ {
		c := sql[i]
		if c == '\'' {
			quoted = !quoted
		}
		if !quoted && isCastAt(sql, i) {
			open := strings.IndexByte(sql[i:], '(') + i
			end := open + closing(sql[open:])
			if end > open {
				if operand, ok := boolCastOperand(sql[open+1 : end]); ok {
					b.WriteString("pg_bool(" + rewriteBoolCasts(operand) + ")")
					i = end
					continue
				}
			}
		}
		b.WriteByte(c)
	}
	return b.String()
}

// isCastAt reports whether the word CAST, followed by its parenthesis, starts
// at i.
func isCastAt(sql string, i int) bool {
	if i+4 > len(sql) || !strings.EqualFold(sql[i:i+4], "CAST") {
		return false
	}
	if i > 0 && isOperandChar(sql[i-1]) {
		return false
	}
	rest := strings.TrimLeft(sql[i+4:], " \t\r\n")
	return strings.HasPrefix(rest, "(")
}

// boolCastOperand is the operand of a CAST's inside "x AS type" when the type
// is a boolean.
func boolCastOperand(inside string) (string, bool) {
	at := lastTopLevelAS(inside)
	if at < 0 {
		return "", false
	}
	switch strings.ToLower(strings.TrimSpace(inside[at+2:])) {
	case "bool", "boolean", "pg_catalog.bool", "pg_catalog.boolean":
		return strings.TrimSpace(inside[:at]), true
	}
	return "", false
}

// lastTopLevelAS is the index of the last AS keyword outside parentheses and
// quotes, or -1.
func lastTopLevelAS(s string) int {
	depth, quoted, last := 0, false, -1
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case depth == 0 && i+2 <= len(s) && strings.EqualFold(s[i:i+2], "AS") &&
			(i == 0 || !isOperandChar(s[i-1])) && (i+2 == len(s) || !isOperandChar(s[i+2])):
			last = i
		}
	}
	return last
}
