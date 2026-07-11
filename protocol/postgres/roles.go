package postgres

import (
	"fmt"
	"strings"

	"overlite/core"
)

// tryRoleDDL handles CREATE/ALTER/DROP ROLE|USER|GROUP by translating them to
// the engine's internal _overlite_roles table (so they succeed and show up in
// \du / pg_roles). GRANT/REVOKE are no-ops handled in interceptUtility, since
// SQLite has no per-object privileges.
func (s *session) tryRoleDDL(sql string) (tag string, handled bool, err error) {
	fields := strings.Fields(sql)
	if len(fields) < 2 {
		return "", false, nil
	}
	verb := strings.ToUpper(fields[0])
	obj := strings.ToUpper(strings.TrimRight(fields[1], ";"))
	isRole := obj == "ROLE" || obj == "USER" || obj == "GROUP"

	switch verb {
	case "CREATE":
		if !isRole {
			return "", false, nil
		}
		return "CREATE ROLE", true, s.createRole(sql, fields, obj)
	case "ALTER":
		if obj == "DEFAULT" { // ALTER DEFAULT PRIVILEGES — accept, no-op
			return "ALTER DEFAULT PRIVILEGES", true, nil
		}
		if !isRole {
			return "", false, nil
		}
		return "ALTER ROLE", true, s.alterRole(sql, fields)
	case "DROP":
		if !isRole {
			return "", false, nil
		}
		return "DROP ROLE", true, s.dropRole(fields)
	}
	return "", false, nil
}

func (s *session) createRole(sql string, fields []string, obj string) error {
	if len(fields) < 3 {
		return fmt.Errorf("syntax error in CREATE %s", obj)
	}
	name := unquoteIdent(fields[2])
	attrs := parseRoleAttrs(fields[3:])
	if _, set := attrs["rolcanlogin"]; !set && obj == "USER" {
		attrs["rolcanlogin"] = 1 // CREATE USER defaults to LOGIN
	}

	cols := []string{"rolname"}
	ph := []string{"?"}
	vals := []core.Value{name}
	for col, v := range attrs {
		cols = append(cols, col)
		ph = append(ph, "?")
		vals = append(vals, v)
	}
	if pw, ok := extractPassword(sql); ok {
		verifier, err := buildSCRAMVerifier(pw)
		if err != nil {
			return err
		}
		cols = append(cols, "rolpassword")
		ph = append(ph, "?")
		vals = append(vals, verifier)
	}
	q := "INSERT INTO _overlite_roles (" + strings.Join(cols, ",") + ") VALUES (" + strings.Join(ph, ",") + ")"
	_, err := s.exec(q, vals)
	return err
}

func (s *session) alterRole(sql string, fields []string) error {
	if len(fields) < 3 {
		return fmt.Errorf("syntax error in ALTER ROLE")
	}
	name := unquoteIdent(fields[2])

	// ALTER ROLE x RENAME TO y
	if len(fields) >= 6 && strings.EqualFold(fields[3], "rename") && strings.EqualFold(fields[4], "to") {
		_, err := s.exec("UPDATE _overlite_roles SET rolname = ? WHERE rolname = ?",
			[]core.Value{unquoteIdent(fields[5]), name})
		return err
	}

	set := make([]string, 0, 2)
	vals := make([]core.Value, 0, 3)
	if pw, ok := extractPassword(sql); ok {
		verifier, err := buildSCRAMVerifier(pw)
		if err != nil {
			return err
		}
		set = append(set, "rolpassword = ?")
		vals = append(vals, verifier)
	}
	for col, v := range parseRoleAttrs(fields[3:]) {
		set = append(set, col+" = ?")
		vals = append(vals, v)
	}
	if len(set) == 0 {
		return nil // ALTER ROLE ... SET config / other — accept as no-op
	}
	vals = append(vals, name)
	_, err := s.exec("UPDATE _overlite_roles SET "+strings.Join(set, ", ")+" WHERE rolname = ?", vals)
	return err
}

// extractPassword pulls the string after PASSWORD / ENCRYPTED PASSWORD out of a
// CREATE/ALTER ROLE statement.
func extractPassword(sql string) (string, bool) {
	low := strings.ToLower(sql)
	i := strings.Index(low, "password")
	if i < 0 {
		return "", false
	}
	p := i + len("password")
	for p < len(sql) && (sql[p] == ' ' || sql[p] == '\t') {
		p++
	}
	if p < len(sql) && sql[p] == '\'' {
		end := endOfStringLiteral(sql, p)
		return strings.ReplaceAll(sql[p+1:end-1], "''", "'"), true
	}
	return "", false
}

func (s *session) dropRole(fields []string) error {
	rest := fields[2:]
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		rest = rest[2:]
	}
	for _, name := range strings.Split(strings.Join(rest, " "), ",") {
		name = unquoteIdent(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if _, err := s.exec("DELETE FROM _overlite_roles WHERE rolname = ?", []core.Value{name}); err != nil {
			return err
		}
	}
	return nil
}

// parseRoleAttrs maps the role option keywords that were explicitly given to
// their column/value, so ALTER only touches what was named.
func parseRoleAttrs(tokens []string) map[string]int {
	m := map[string]int{}
	for _, t := range tokens {
		switch strings.ToUpper(strings.TrimRight(t, ";")) {
		case "SUPERUSER":
			m["rolsuper"] = 1
		case "NOSUPERUSER":
			m["rolsuper"] = 0
		case "CREATEDB":
			m["rolcreatedb"] = 1
		case "NOCREATEDB":
			m["rolcreatedb"] = 0
		case "CREATEROLE":
			m["rolcreaterole"] = 1
		case "NOCREATEROLE":
			m["rolcreaterole"] = 0
		case "LOGIN":
			m["rolcanlogin"] = 1
		case "NOLOGIN":
			m["rolcanlogin"] = 0
		case "INHERIT":
			m["rolinherit"] = 1
		case "NOINHERIT":
			m["rolinherit"] = 0
		case "REPLICATION":
			m["rolreplication"] = 1
		case "NOREPLICATION":
			m["rolreplication"] = 0
		case "BYPASSRLS":
			m["rolbypassrls"] = 1
		case "NOBYPASSRLS":
			m["rolbypassrls"] = 0
		}
	}
	return m
}
