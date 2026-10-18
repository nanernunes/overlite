package postgres

import (
	"strings"
)

// A Query message may carry several statements separated by semicolons, and
// Postgres runs them one after another, each with its own result, inside one
// implicit transaction: the first failure stops the rest and undoes what came
// before. Migration tools depend on that — Prisma applies a whole migration
// file as one Query. Handing the whole string to the router instead had it
// classify the batch by its first statement: a migration opening with
// CREATE SCHEMA was taken for that statement alone and the tables after it
// were never created, with no error to say so.

// handleQuery runs a Query message, statement by statement.
func (s *session) handleQuery(body []byte) error {
	stmts := splitStatements(strings.TrimRight(string(body), "\x00"))
	if len(stmts) <= 1 {
		return s.handleSimpleQuery(body)
	}

	// Statements that manage transactions themselves (or, like CREATE
	// DATABASE, refuse to run inside one) opt the batch out of the implicit
	// transaction; each then runs on its own, as before.
	implicit := s.tx == nil
	for _, st := range stmts {
		st = stripLeadingComments(st)
		if txControlKind(st) != "" || savepointKind(st) != "" || isDatabaseDDL(st) {
			implicit = false
		}
	}
	if implicit {
		if _, err := s.applyTxControl("BEGIN"); err != nil {
			return s.c.sendError("25000", err.Error())
		}
	}

	failed := false
	for _, st := range stmts {
		s.c.errored = false
		if err := s.handleSimpleQuery([]byte(st)); err != nil {
			return err
		}
		if s.c.errored {
			failed = true
			break
		}
	}

	if implicit && s.tx != nil {
		if err := s.endTx(!failed); err != nil && !failed {
			return s.sendExecError(err)
		}
	}
	return nil
}

// splitStatements cuts sql at the semicolons that end statements, leaving
// alone those inside string literals, quoted identifiers, dollar-quoted bodies
// and comments. Pieces with nothing executable in them are dropped.
func splitStatements(sql string) []string {
	var out []string
	add := func(st string) {
		if !isBlankStatement(st) {
			out = append(out, strings.TrimSpace(st))
		}
	}
	start := 0
	for i := 0; i < len(sql); i++ {
		switch c := sql[i]; {
		case c == '\'':
			if i > 0 && (sql[i-1] == 'e' || sql[i-1] == 'E') && (i < 2 || !isWordByte(sql[i-2])) {
				i = skipEscapeString(sql, i) - 1
			} else {
				i = skipQuoted(sql, i, '\'') - 1
			}
		case c == '"':
			i = skipQuoted(sql, i, '"') - 1
		case c == '-' && i+1 < len(sql) && sql[i+1] == '-':
			i = skipLineComment(sql, i) - 1
		case c == '/' && i+1 < len(sql) && sql[i+1] == '*':
			j, _ := skipBlockComment(sql, i)
			i = j - 1
		case c == '$':
			if tag := dollarTag(sql, i); tag != "" {
				if j := strings.Index(sql[i+len(tag):], tag); j >= 0 {
					i += len(tag) + j + len(tag) - 1
				} else {
					i = len(sql) - 1
				}
			}
		case c == ';':
			if openBody(sql[start:i]) {
				continue // a semicolon inside a trigger's BEGIN … END
			}
			add(sql[start:i])
			start = i + 1
		}
	}
	add(sql[start:])
	return out
}

// openBody reports whether stmt is a CREATE TRIGGER, FUNCTION or PROCEDURE
// whose BEGIN … END body (SQLite's trigger form, or BEGIN ATOMIC) is still
// open, so a semicolon at its end belongs to the body. CASE … END is counted
// too, since the same END closes it.
func openBody(stmt string) bool {
	f := strings.Fields(strings.ToUpper(stripLeadingComments(stmt)))
	if len(f) < 2 || f[0] != "CREATE" {
		return false
	}
	f = f[1:]
	if len(f) > 2 && f[0] == "OR" && f[1] == "REPLACE" {
		f = f[2:]
	}
	if len(f) > 1 && (f[0] == "TEMP" || f[0] == "TEMPORARY") {
		f = f[1:]
	}
	if len(f) == 0 || (f[0] != "TRIGGER" && f[0] != "FUNCTION" && f[0] != "PROCEDURE") {
		return false
	}
	low := strings.ToLower(stmt)
	depth := 0
	for i := 0; i < len(stmt); i++ {
		switch c := stmt[i]; {
		case c == '\'' || c == '"':
			i = skipQuoted(stmt, i, c) - 1
		case c == '-' && i+1 < len(stmt) && stmt[i+1] == '-':
			i = skipLineComment(stmt, i) - 1
		case c == '/' && i+1 < len(stmt) && stmt[i+1] == '*':
			j, _ := skipBlockComment(stmt, i)
			i = j - 1
		case c == '$':
			if tag := dollarTag(stmt, i); tag != "" {
				if j := strings.Index(stmt[i+len(tag):], tag); j >= 0 {
					i += len(tag) + j + len(tag) - 1
				} else {
					return false
				}
			}
		case isWordByte(c) && (i == 0 || !isWordByte(stmt[i-1])):
			j := i
			for j < len(stmt) && isWordByte(stmt[j]) {
				j++
			}
			switch low[i:j] {
			case "begin", "case":
				depth++
			case "end":
				depth--
			}
			i = j - 1
		}
	}
	return depth > 0
}

// dollarTag returns the $tag$ (or $$) opening a dollar-quoted string at i, or
// "" when the $ starts something else, such as the parameter $1.
func dollarTag(sql string, i int) string {
	if i > 0 && isWordByte(sql[i-1]) {
		return "" // part of an identifier, e.g. a$b
	}
	j := i + 1
	for j < len(sql) && sql[j] != '$' {
		if !isWordByte(sql[j]) || (j == i+1 && sql[j] >= '0' && sql[j] <= '9') {
			return ""
		}
		j++
	}
	if j >= len(sql) {
		return ""
	}
	return sql[i : j+1]
}

// skipEscapeString returns the index just past the E'…' string whose quote is
// at i, where a backslash escapes the character after it.
func skipEscapeString(sql string, i int) int {
	for j := i + 1; j < len(sql); j++ {
		switch sql[j] {
		case '\\':
			j++
		case '\'':
			if j+1 < len(sql) && sql[j+1] == '\'' {
				j++
				continue
			}
			return j + 1
		}
	}
	return len(sql)
}
