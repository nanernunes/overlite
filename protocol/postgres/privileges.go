package postgres

import (
	"fmt"
	"strings"

	"overlite/core"
)

// Privilege enforcement. SQLite has no per-object privileges, so overlite keeps
// grants in _overlite_grants and table ownership in _overlite_owners, and this
// layer checks a statement against the connected role before it runs.
//
// Enforcement is deliberately narrow to stay safe and backward compatible:
//   - A superuser, or a role not managed by overlite (never CREATE ROLE'd), is
//     never checked — existing single-user setups keep working unchanged.
//   - A table's owner holds every privilege on it implicitly, and alone may
//     DROP/ALTER it.
//   - Otherwise the role needs a matching GRANT (to itself or to PUBLIC).
//
// The table extraction is statement-level (best effort): the primary target of
// a DML statement and its FROM/JOIN tables. Comma-joined tables and correlated
// subqueries may be under-checked, never over-checked.

type tablePriv struct {
	table string
	priv  string // SELECT/INSERT/UPDATE/DELETE/TRUNCATE, or OWNER for DROP/ALTER
}

// checkPrivileges rejects the statement if the session's current role lacks a
// required privilege. It returns nil for bypassing roles and table-less
// statements.
func (s *session) checkPrivileges(sql string) error {
	reqs := requiredPrivileges(sql)
	if len(reqs) == 0 || s.privBypass() {
		return nil
	}
	for _, r := range reqs {
		if !s.hasPrivilege(r.table, r.priv) {
			return fmt.Errorf("permission denied for table %s", r.table)
		}
	}
	return nil
}

// privBypass reports whether the current role skips privilege checks: unmanaged
// roles (never CREATE ROLE'd) and superusers. Cached per role; the cache is
// cleared on role DDL (see clearPrivCache).
func (s *session) privBypass() bool {
	if s.bypassCache == nil {
		s.bypassCache = map[string]bool{}
	}
	if b, ok := s.bypassCache[s.currentRole]; ok {
		return b
	}
	b := s.computeBypass()
	s.bypassCache[s.currentRole] = b
	return b
}

func (s *session) computeBypass() bool {
	rs, err := s.exec("SELECT rolsuper FROM _overlite_roles WHERE lower(rolname) = lower("+
		sqlStr(s.currentRole)+")", nil)
	if err != nil || len(rs.Rows) == 0 {
		return true // unmanaged role -> trusted/legacy
	}
	return truthy(rs.Rows[0][0])
}

func (s *session) clearPrivCache() { s.bypassCache = nil }

// hasPrivilege checks ownership then (for grantable privileges) the grant table,
// evaluated against the role's effective set (itself plus inherited roles).
func (s *session) hasPrivilege(table, priv string) bool {
	roles := s.effectiveRoles()
	if s.ownsTable(table, roles) {
		return true
	}
	if priv == "OWNER" {
		return false // only the owner (or a role that inherits it) qualifies
	}
	grantees := inList(append(append([]string{}, roles...), "public"))
	rs, err := s.exec("SELECT 1 FROM _overlite_grants WHERE lower(tablename) = lower("+
		sqlStr(table)+") AND upper(privilege) IN (upper("+sqlStr(priv)+"), 'ALL')"+
		" AND lower(grantee) IN ("+grantees+") LIMIT 1", nil)
	return err == nil && len(rs.Rows) > 0
}

func (s *session) ownsTable(table string, roles []string) bool {
	rs, err := s.exec("SELECT 1 FROM _overlite_owners WHERE lower(tablename) = lower("+
		sqlStr(table)+") AND lower(owner) IN ("+inList(roles)+") LIMIT 1", nil)
	return err == nil && len(rs.Rows) > 0
}

// effectiveRoles returns the current role plus every role it inherits from,
// transitively — following a membership edge only when the member has INHERIT.
// All names are lower-cased.
func (s *session) effectiveRoles() []string {
	cur := strings.ToLower(s.currentRole)
	roles := []string{cur}
	rs, err := s.exec(`WITH RECURSIVE eff(role) AS (
  SELECT lower(`+sqlStr(s.currentRole)+`)
  UNION
  SELECT lower(m.roleof) FROM _overlite_memberships m
    JOIN eff e ON lower(m.member) = e.role
    JOIN _overlite_roles r ON lower(r.rolname) = e.role AND r.rolinherit <> 0
) SELECT role FROM eff`, nil)
	if err != nil {
		return roles
	}
	seen := map[string]bool{cur: true}
	for _, row := range rs.Rows {
		if str, ok := row[0].(string); ok && !seen[str] {
			seen[str] = true
			roles = append(roles, str)
		}
	}
	return roles
}

// inList renders role names as a lower-cased SQL IN-list body.
func inList(roles []string) string {
	parts := make([]string, len(roles))
	for i, r := range roles {
		parts[i] = sqlStr(strings.ToLower(r))
	}
	return strings.Join(parts, ", ")
}

// requiredPrivileges returns the (table, privilege) pairs a statement needs.
func requiredPrivileges(sql string) []tablePriv {
	switch firstWordUpper(sql) {
	case "SELECT", "WITH", "TABLE":
		return tablesAfter(sql, []string{"from", "join"}, "SELECT")
	case "INSERT":
		return tablesAfter(sql, []string{"into"}, "INSERT")
	case "UPDATE":
		return tablesAfter(sql, []string{"update"}, "UPDATE")
	case "DELETE":
		return tablesAfter(sql, []string{"from"}, "DELETE")
	case "TRUNCATE":
		return truncateTables(sql)
	case "DROP", "ALTER":
		if strings.EqualFold(secondWord(sql), "table") {
			return tablesAfter(sql, []string{"table"}, "OWNER")
		}
	}
	return nil
}

// tablesAfter collects the table reference following each whole-word occurrence
// of a keyword (outside string literals), tagged with priv. System catalog and
// internal tables are skipped.
func tablesAfter(sql string, keywords []string, priv string) []tablePriv {
	kw := map[string]bool{}
	for _, k := range keywords {
		kw[k] = true
	}
	var out []tablePriv
	seen := map[string]bool{}
	toks := scanWords(sql)
	for i := 0; i < len(toks)-1; i++ {
		if !kw[strings.ToLower(toks[i].text)] || toks[i].quoted {
			continue
		}
		name, ok := tableName(toks[i+1])
		if !ok || seen[strings.ToLower(name)] {
			continue
		}
		seen[strings.ToLower(name)] = true
		out = append(out, tablePriv{name, priv})
	}
	return out
}

// truncateTables parses "TRUNCATE [TABLE] t1, t2, ..." into TRUNCATE privileges.
func truncateTables(sql string) []tablePriv {
	toks := scanWords(sql)
	var out []tablePriv
	for i := 1; i < len(toks); i++ { // skip the leading TRUNCATE
		if strings.EqualFold(toks[i].text, "table") || strings.EqualFold(toks[i].text, "only") {
			continue
		}
		if name, ok := tableName(toks[i]); ok {
			out = append(out, tablePriv{name, "TRUNCATE"})
		}
	}
	return out
}

// tableName turns a table-reference token into a bare, enforceable table name.
// It returns ok=false for subqueries, keywords, and system/internal tables.
func tableName(t word) (string, bool) {
	if t.text == "" || t.text[0] == '(' {
		return "", false
	}
	name := t.text
	schema := ""
	if dot := strings.IndexByte(name, '.'); dot >= 0 {
		schema, name = name[:dot], name[dot+1:]
	}
	name = strings.Trim(name, `"`)
	schema = strings.Trim(schema, `"`)
	if name == "" {
		return "", false
	}
	low := strings.ToLower(name)
	if !t.quoted && sqlReserved[low] {
		return "", false
	}
	if strings.EqualFold(schema, "pg_catalog") || strings.EqualFold(schema, "information_schema") {
		return "", false
	}
	if strings.HasPrefix(low, "pg_") || strings.HasPrefix(low, "sqlite_") ||
		strings.HasPrefix(low, "_overlite") || low == "information_schema" {
		return "", false
	}
	return name, true
}

// sqlReserved is the small set of words that can legally follow FROM/JOIN/etc.
// without being a table (so we don't treat them as one).
var sqlReserved = map[string]bool{
	"select": true, "where": true, "on": true, "using": true, "set": true,
	"values": true, "lateral": true, "only": true, "as": true, "group": true,
	"order": true, "limit": true, "returning": true, "natural": true,
	"left": true, "right": true, "inner": true, "outer": true, "full": true,
	"cross": true, "join": true,
}

// word is an identifier/punctuation token from scanWords.
type word struct {
	text   string
	quoted bool // came from a "double-quoted" identifier
}

// scanWords splits a statement into identifier tokens (including a leading
// schema-qualifier and double-quoted parts), lone "(" markers, and skips string
// literals. It is purpose-built for table extraction, not a general lexer.
func scanWords(sql string) []word {
	var out []word
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'':
			i = endOfStringLiteral(sql, i)
		case c == '(':
			out = append(out, word{text: "("})
			i++
		case c == '"' || isIdentStart(c) || c == '.':
			j, quoted := i, false
			for j < len(sql) {
				d := sql[j]
				if d == '"' {
					quoted = true
					j++
					for j < len(sql) && sql[j] != '"' {
						j++
					}
					if j < len(sql) {
						j++
					}
					continue
				}
				if isIdentPart(d) || d == '.' {
					j++
					continue
				}
				break
			}
			out = append(out, word{text: sql[i:j], quoted: quoted})
			i = j
		default:
			i++
		}
	}
	return out
}

func secondWord(sql string) string {
	f := strings.Fields(sql)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func truthy(v core.Value) bool {
	switch x := v.(type) {
	case int64:
		return x != 0
	case int:
		return x != 0
	case bool:
		return x
	case float64:
		return x != 0
	case string:
		return x != "" && x != "0" && !strings.EqualFold(x, "false")
	}
	return false
}
