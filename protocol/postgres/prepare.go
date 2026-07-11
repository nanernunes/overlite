package postgres

import "strings"

// SQL-level prepared statements: PREPARE name [(types)] AS <query>, then
// EXECUTE name [(args)], then DEALLOCATE name. psql and pg_dump use these (e.g.
// pg_dump prepares a per-enum label query and executes it for each enum type).
// This is separate from the wire Extended-Query prepared statements.

// trySQLPrepare handles PREPARE/EXECUTE/DEALLOCATE in the simple-query path.
// It returns handled=true when it recognized the statement.
func (s *session) trySQLPrepare(sql string) (handled bool, err error) {
	switch firstWordUpper(sql) {
	case "PREPARE":
		return true, s.handlePrepare(sql)
	case "EXECUTE":
		return true, s.handleExecuteSQL(sql)
	case "DEALLOCATE":
		if fields := strings.Fields(sql); len(fields) >= 2 {
			delete(s.sqlPrepared, unquoteIdent(fields[1]))
		}
		return true, s.c.sendCommandComplete("DEALLOCATE")
	}
	return false, nil
}

// handlePrepare stores "PREPARE name [(types)] AS <query>".
func (s *session) handlePrepare(sql string) error {
	rest := strings.TrimSpace(sql[len("prepare"):])
	name := readIdent(rest)
	if name == "" {
		return s.c.sendError("42601", "syntax error in PREPARE")
	}
	// The query begins after the top-level " AS " (any argument-type list is in
	// parens, so the first depth-0 AS is the separator).
	as := indexTopLevelWord(sql, len("prepare"), "as")
	if as < 0 {
		return s.c.sendError("42601", "PREPARE requires AS <query>")
	}
	s.sqlPrepared[name] = strings.TrimSpace(sql[as+len("as"):])
	return s.c.sendCommandComplete("PREPARE")
}

// handleExecuteSQL runs a prepared statement, substituting its $N parameters
// with the EXECUTE arguments.
func (s *session) handleExecuteSQL(sql string) error {
	rest := strings.TrimSpace(sql[len("execute"):])
	name := readIdent(rest)
	query, ok := s.sqlPrepared[name]
	if !ok {
		return s.c.sendError("26000", "prepared statement "+quoteName(name)+" does not exist")
	}
	var args []string
	if i := strings.IndexByte(rest, '('); i >= 0 {
		if inner, _, okp := readParenArgs(rest, i); okp {
			for _, a := range splitTopLevel(inner) {
				args = append(args, strings.TrimSpace(a))
			}
		}
	}
	final := substituteParams(query, args)

	rewritten, err := s.rewriteForExec(final)
	if err != nil {
		return s.sendExecError(err)
	}
	rs, err := s.exec(rewritten, nil)
	if err != nil {
		logQueryError("execute-sql", err, final, rewritten)
		return s.sendExecError(err)
	}
	if rs.IsQuery {
		if err := s.c.sendResultSet(rs); err != nil {
			return err
		}
	}
	return s.c.sendCommandComplete(commandTag(rs))
}

// substituteParams replaces $1, $2, ... in query with the given args (already
// SQL literals), skipping placeholders inside string literals.
func substituteParams(query string, args []string) string {
	var b strings.Builder
	for i := 0; i < len(query); i++ {
		if query[i] == '\'' {
			j := endOfStringLiteral(query, i)
			b.WriteString(query[i:j])
			i = j - 1
			continue
		}
		if query[i] == '$' && i+1 < len(query) && query[i+1] >= '1' && query[i+1] <= '9' {
			j, n := i+1, 0
			for j < len(query) && query[j] >= '0' && query[j] <= '9' {
				n = n*10 + int(query[j]-'0')
				j++
			}
			if n >= 1 && n <= len(args) {
				b.WriteString(args[n-1])
				i = j - 1
				continue
			}
		}
		b.WriteByte(query[i])
	}
	return b.String()
}

// readIdent reads a leading (optionally "quoted") identifier from s.
func readIdent(s string) string {
	s = strings.TrimLeft(s, " \t\r\n")
	if s == "" {
		return ""
	}
	if s[0] == '"' {
		if j := strings.IndexByte(s[1:], '"'); j >= 0 {
			return s[1 : 1+j]
		}
		return ""
	}
	j := 0
	for j < len(s) && isIdentPart(s[j]) {
		j++
	}
	return s[:j]
}
