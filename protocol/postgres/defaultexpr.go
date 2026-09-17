package postgres

import (
	"strings"
)

// SQLite only accepts a bare literal or a keyword like CURRENT_TIMESTAMP as a
// column default; anything computed has to be parenthesised. Postgres needs no
// parentheses, so `DEFAULT now()` and `DEFAULT gen_random_uuid()` reached
// SQLite as a syntax error on the opening bracket.

// rewriteDefaultExpr wraps a function-call default in the parentheses SQLite
// requires. A default that is already parenthesised, a literal, or a bare
// keyword is left alone.
//
// The scan walks the whole statement rather than only its non-string parts:
// the call's own arguments are usually string literals (`datetime('now')`),
// so splitting on them would hide the call.
func rewriteDefaultExpr(sql string) string {
	if !strings.Contains(strings.ToUpper(sql), "DEFAULT") {
		return sql
	}
	var b strings.Builder
	i := 0
	for i < len(sql) {
		c := sql[i]
		if c == '\'' || c == '"' {
			j := skipQuoted(sql, i, c)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if (c == 'd' || c == 'D') && isDefaultKeyword(sql, i) {
			if wrapped, end, ok := wrapDefaultCall(sql, i); ok {
				b.WriteString(wrapped)
				i = end
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// isDefaultKeyword reports whether the word DEFAULT starts at i.
func isDefaultKeyword(sql string, i int) bool {
	const kw = "DEFAULT"
	if i+len(kw) > len(sql) || !strings.EqualFold(sql[i:i+len(kw)], kw) {
		return false
	}
	if i > 0 && isWordByte(sql[i-1]) {
		return false
	}
	return i+len(kw) == len(sql) || !isWordByte(sql[i+len(kw)])
}

// wrapDefaultCall renders `DEFAULT f(...)` as `DEFAULT (f(...))`, returning the
// index just past the call. It reports false when the default is not a call.
func wrapDefaultCall(sql string, i int) (text string, end int, ok bool) {
	j := i + len("DEFAULT")
	for j < len(sql) && (sql[j] == ' ' || sql[j] == '\t' || sql[j] == '\n' || sql[j] == '\r') {
		j++
	}
	name := j
	for j < len(sql) && isWordByte(sql[j]) {
		j++
	}
	if j == name {
		return "", 0, false // not an identifier: a literal, or already "("
	}
	k := j
	for k < len(sql) && sql[k] == ' ' {
		k++
	}
	if k >= len(sql) || sql[k] != '(' {
		return "", 0, false // a bare keyword such as CURRENT_TIMESTAMP
	}
	close, found := matchParen(sql, k)
	if !found {
		return "", 0, false
	}
	return sql[i:name] + "(" + sql[name:close] + ")", close, true
}

// matchParen returns the index just past the parenthesis that closes the one
// at i.
func matchParen(s string, i int) (end int, ok bool) {
	depth := 0
	for i < len(s) {
		switch s[i] {
		case '\'':
			i = skipQuoted(s, i, '\'')
			continue
		case '"':
			i = skipQuoted(s, i, '"')
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, true
			}
		}
		i++
	}
	return 0, false
}

// skipQuoted returns the index just past the quoted run starting at i.
func skipQuoted(s string, i int, q byte) int {
	i++
	for i < len(s) {
		if s[i] == q {
			if i+1 < len(s) && s[i+1] == q {
				i += 2
				continue
			}
			return i + 1
		}
		i++
	}
	return len(s)
}
