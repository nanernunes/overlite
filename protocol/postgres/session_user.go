package postgres

import (
	"fmt"
	"strings"
)

// Session identity: current_user / session_user / current_role reflect the
// connecting role (and SET ROLE / SET SESSION AUTHORIZATION). Because the engine
// scalar functions are process-global, we substitute these niladic functions
// with the session's role literal before the statement runs.

// expandSessionUser replaces current_user / current_role / session_user (bare or
// called) with the session's role as a string literal, outside string literals
// and skipping quoted/qualified identifiers.
func (s *session) expandSessionUser(sql string) string {
	low := strings.ToLower(sql)
	if !strings.Contains(low, "_user") && !strings.Contains(low, "current_role") {
		return sql
	}
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			j := endOfStringLiteral(sql, i)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if !isIdentStart(c) {
			b.WriteByte(c)
			i++
			continue
		}
		// A qualified ("n.current_user") or quoted identifier is not the function.
		qualified := i > 0 && (sql[i-1] == '.' || sql[i-1] == '"' || isIdentPart(sql[i-1]))
		j := i
		for j < len(sql) && isIdentPart(sql[j]) {
			j++
		}
		lit, ok := s.userLiteral(strings.ToLower(sql[i:j]))
		if !ok || qualified {
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		// Consume an optional "()" call suffix.
		if k := skipSpaces(sql, j); k < len(sql) && sql[k] == '(' {
			if m := skipSpaces(sql, k+1); m < len(sql) && sql[m] == ')' {
				j = m + 1
			}
		}
		b.WriteString(lit)
		i = j
	}
	return b.String()
}

func (s *session) userLiteral(word string) (string, bool) {
	switch word {
	case "current_user", "current_role":
		return sqlStr(s.currentRole), true
	case "session_user":
		return sqlStr(s.sessionUser), true
	}
	return "", false
}

// isSetRole reports whether sql is a SET/RESET ROLE or SET/RESET SESSION
// AUTHORIZATION statement (handled by the session, not passed to SQLite).
func isSetRole(sql string) bool {
	f := strings.Fields(sql)
	if len(f) < 2 {
		return false
	}
	switch strings.ToUpper(f[0]) {
	case "SET", "RESET":
		return strings.EqualFold(f[1], "role") ||
			(len(f) >= 3 && strings.EqualFold(f[1], "session") && strings.EqualFold(f[2], "authorization"))
	}
	return false
}

// applySetRole updates the session identity for a SET/RESET ROLE / SESSION
// AUTHORIZATION statement.
func (s *session) applySetRole(sql string) error {
	f := strings.Fields(sql)
	reset := strings.EqualFold(f[0], "reset")

	if strings.EqualFold(f[1], "role") {
		if reset {
			s.currentRole = s.sessionUser
			return nil
		}
		target := roleTarget(f[2:])
		if target == "" || strings.EqualFold(target, "none") || strings.EqualFold(target, "default") {
			s.currentRole = s.sessionUser
			return nil
		}
		if !s.roleExists(target) {
			return fmt.Errorf("role %q does not exist", target)
		}
		s.currentRole = target
		return nil
	}

	// SET/RESET SESSION AUTHORIZATION
	if reset {
		s.sessionUser, s.currentRole = s.authUser, s.authUser
		return nil
	}
	target := roleTarget(f[3:]) // after "session authorization"
	if target == "" || strings.EqualFold(target, "default") {
		s.sessionUser, s.currentRole = s.authUser, s.authUser
		return nil
	}
	if !s.roleExists(target) {
		return fmt.Errorf("role %q does not exist", target)
	}
	s.sessionUser, s.currentRole = target, target
	return nil
}

// roleTarget reads the role name after an optional TO / = from the tokens.
func roleTarget(toks []string) string {
	for len(toks) > 0 && (strings.EqualFold(toks[0], "to") || toks[0] == "=") {
		toks = toks[1:]
	}
	if len(toks) == 0 {
		return ""
	}
	t := strings.TrimRight(toks[0], ";")
	if len(t) >= 2 && (t[0] == '\'' || t[0] == '"') {
		return t[1 : len(t)-1]
	}
	return t
}

func (s *session) roleExists(name string) bool {
	rs, err := s.exec("SELECT 1 FROM _overlite_roles WHERE rolname = "+sqlStr(name)+" COLLATE NOCASE", nil)
	return err == nil && len(rs.Rows) > 0
}
