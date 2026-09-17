package postgres

import "strings"

// A statement that carries no executable SQL — a lone comment, say — is not the
// same as one the engine can run. Postgres answers it with EmptyQueryResponse,
// and clients rely on that: database/sql's Ping sends the bare comment
// `-- ping`, so every pgx-backed client opens a connection with one.
//
// The engine must never see these. modernc.org/sqlite returns a nil
// driver.Result for a statement it finds nothing to run in, which
// database/sql wraps and then panics on when asked for RowsAffected.

// isBlankStatement reports whether sql contains nothing executable: only
// whitespace, semicolons and comments.
func isBlankStatement(sql string) bool {
	i := 0
	for i < len(sql) {
		c := sql[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\v' || c == '\f' || c == ';':
			i++
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLineComment(sql, i)
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j, closed := skipBlockComment(sql, i)
			if !closed {
				// Unterminated: let the parser reject it, as Postgres does,
				// rather than passing it off as an empty statement.
				return false
			}
			i = j
		default:
			return false
		}
	}
	return true
}

// skipLineComment returns the index of the newline ending the `--` comment at
// i, or the end of the string.
func skipLineComment(sql string, i int) int {
	if j := strings.IndexByte(sql[i:], '\n'); j >= 0 {
		return i + j + 1
	}
	return len(sql)
}

// skipBlockComment returns the index just past the `/* */` comment at i, and
// whether it was closed. Postgres nests these, so an inner /* must be matched
// before the outer */.
func skipBlockComment(sql string, i int) (end int, closed bool) {
	depth := 0
	for i < len(sql) {
		switch {
		case strings.HasPrefix(sql[i:], "/*"):
			depth++
			i += 2
		case strings.HasPrefix(sql[i:], "*/"):
			depth--
			i += 2
			if depth == 0 {
				return i, true
			}
		default:
			i++
		}
	}
	return len(sql), false
}
