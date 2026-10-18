package postgres

import "strings"

// Postgres takes OFFSET on its own, and before or after LIMIT. SQLite only has
// OFFSET as a part of LIMIT (`LIMIT n OFFSET m`, in that order), so it rejects
// a bare `OFFSET m` with a syntax error. ORMs lean on the bare form: Prisma
// ends nearly every read it sends with `OFFSET $n`, subqueries included.

// rewriteOffset gives each OFFSET that has no LIMIT in front of it the LIMIT
// SQLite requires: the one written after it, moved in front (`OFFSET 5 LIMIT
// 10` becomes `LIMIT 10 OFFSET 5`), or else `LIMIT -1`, which SQLite reads as
// no limit at all. Each parenthesized level is a query of its own.
func rewriteOffset(sql string) string {
	low := strings.ToLower(sql)
	if !strings.Contains(low, "offset") {
		return sql
	}
	type edit struct {
		start, end int
		text       string
	}
	var edits []edit
	limited := []bool{false} // per paren depth: the query there has its LIMIT
	for i := 0; i < len(sql); i++ {
		switch c := sql[i]; {
		case c == '\'' || c == '"':
			i = skipQuoted(sql, i, c) - 1
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLineComment(sql, i) - 1
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j, _ := skipBlockComment(sql, i)
			i = j - 1
		case c == '(':
			limited = append(limited, false)
		case c == ')':
			if len(limited) > 1 {
				limited = limited[:len(limited)-1]
			}
		case isClauseWord(low, i, "limit"):
			limited[len(limited)-1] = true
		case isClauseWord(low, i, "offset"):
			d := len(limited) - 1
			if limited[d] {
				continue
			}
			limited[d] = true
			offEnd := clauseEnd(sql, low, i+len("offset"))
			if !isClauseWord(low, offEnd, "limit") {
				edits = append(edits, edit{i, i, "LIMIT -1 "})
				continue
			}
			limEnd := clauseEnd(sql, low, offEnd+len("limit"))
			span := sql[i:limEnd]
			trail := span[len(strings.TrimRight(span, " \t\r\n")):]
			text := strings.TrimSpace(sql[offEnd:limEnd]) + " " + strings.TrimSpace(sql[i:offEnd]) + trail
			edits = append(edits, edit{i, limEnd, text})
			i = limEnd - 1
		}
	}
	for k := len(edits) - 1; k >= 0; k-- {
		e := edits[k]
		sql = sql[:e.start] + e.text + sql[e.end:]
	}
	return sql
}

// isClauseWord reports whether the keyword word starts at i as a clause, not
// as part of a longer name, a column reached through a qualifier (t.offset) or
// an output label (AS offset).
func isClauseWord(low string, i int, word string) bool {
	if !strings.HasPrefix(low[i:], word) {
		return false
	}
	if n := i + len(word); n < len(low) && (isWordByte(low[n]) || low[n] == '"') {
		return false
	}
	if i > 0 && (isWordByte(low[i-1]) || low[i-1] == '.' || low[i-1] == '$' || low[i-1] == '"') {
		return false
	}
	before := strings.TrimRight(low[:i], " \t\r\n")
	return !strings.HasSuffix(before, " as") && before != "as"
}

// clauseEnd returns where the clause whose expression starts at from ends: at
// the next clause keyword of the same query, the paren that closes it, a
// semicolon, or the end of the statement.
func clauseEnd(sql, low string, from int) int {
	depth := 0
	for i := from; i < len(sql); i++ {
		switch c := sql[i]; {
		case c == '\'' || c == '"':
			i = skipQuoted(sql, i, c) - 1
		case c == '(':
			depth++
		case c == ')':
			if depth == 0 {
				return i
			}
			depth--
		case c == ';' && depth == 0:
			return i
		case depth == 0:
			for _, w := range []string{"limit", "offset", "fetch", "for", "union", "intersect", "except", "returning"} {
				if isClauseWord(low, i, w) {
					return i
				}
			}
		}
	}
	return len(sql)
}
