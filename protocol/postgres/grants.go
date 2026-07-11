package postgres

import (
	"fmt"
	"strings"
)

// GRANT/REVOKE handling and table-ownership tracking. These write the internal
// _overlite_grants / _overlite_owners tables that privileges.go reads.

// isGrant reports whether sql is a GRANT or REVOKE statement.
func isGrant(sql string) bool {
	w := firstWordUpper(sql)
	return w == "GRANT" || w == "REVOKE"
}

// applyGrant parses a table GRANT/REVOKE and updates _overlite_grants. Role
// (GRANT role TO role) and non-table object grants are accepted as no-ops.
// It returns the command tag ("GRANT"/"REVOKE").
func (s *session) applyGrant(sql string) (string, error) {
	revoke := strings.EqualFold(firstWordUpper(sql), "REVOKE")
	tag := "GRANT"
	if revoke {
		tag = "REVOKE"
	}

	// No ON clause => role membership: GRANT <role> TO <member>.
	if indexWord(strings.ToLower(sql), "on") < 0 {
		return tag, s.applyRoleGrant(sql, revoke)
	}

	privs, tables, grantees, ok := parseGrant(sql, revoke)
	if !ok {
		return tag, nil // shape we don't model (roles, schemas, …): no-op
	}

	for _, tbl := range tables {
		for _, who := range grantees {
			for _, priv := range privs {
				if revoke {
					if _, err := s.exec("DELETE FROM _overlite_grants WHERE lower(grantee)=lower("+
						sqlStr(who)+") AND lower(tablename)=lower("+sqlStr(tbl)+
						") AND (upper(privilege)=upper("+sqlStr(priv)+") OR upper("+sqlStr(priv)+
						")='ALL')", nil); err != nil {
						return tag, err
					}
					continue
				}
				// Avoid duplicate rows for the same (grantee, table, privilege).
				if _, err := s.exec("DELETE FROM _overlite_grants WHERE lower(grantee)=lower("+
					sqlStr(who)+") AND lower(tablename)=lower("+sqlStr(tbl)+
					") AND upper(privilege)=upper("+sqlStr(priv)+")", nil); err != nil {
					return tag, err
				}
				if _, err := s.exec("INSERT INTO _overlite_grants (grantee, tablename, privilege)"+
					" VALUES ("+sqlStr(who)+", "+sqlStr(tbl)+", "+sqlStr(strings.ToUpper(priv))+")", nil); err != nil {
					return tag, err
				}
			}
		}
	}
	return tag, nil
}

// applyRoleGrant records or removes role membership for GRANT <role> TO <member>
// / REVOKE [ADMIN OPTION FOR] <role> FROM <member>. Only a superuser or a holder
// of ADMIN OPTION on the role may administer its membership.
func (s *session) applyRoleGrant(sql string, revoke bool) error {
	rg, ok := parseRoleGrant(sql, revoke)
	if !ok {
		return nil
	}
	for _, role := range rg.roles {
		if !s.canGrantMembership(role) {
			return fmt.Errorf("must have admin option on role %q", role)
		}
		for _, mem := range rg.members {
			if revoke {
				if rg.adminOnly {
					// REVOKE ADMIN OPTION FOR: keep membership, drop the option.
					if _, err := s.exec("UPDATE _overlite_memberships SET admin_option=0 WHERE lower(member)=lower("+
						sqlStr(mem)+") AND lower(roleof)=lower("+sqlStr(role)+")", nil); err != nil {
						return err
					}
					continue
				}
				if _, err := s.exec("DELETE FROM _overlite_memberships WHERE lower(member)=lower("+
					sqlStr(mem)+") AND lower(roleof)=lower("+sqlStr(role)+")", nil); err != nil {
					return err
				}
				continue
			}
			if !s.roleExists(role) {
				return fmt.Errorf("role %q does not exist", role)
			}
			if !s.roleExists(mem) {
				return fmt.Errorf("role %q does not exist", mem)
			}
			admin := "0"
			if rg.admin {
				admin = "1"
			}
			// Idempotent: don't stack duplicate membership rows.
			if _, err := s.exec("DELETE FROM _overlite_memberships WHERE lower(member)=lower("+
				sqlStr(mem)+") AND lower(roleof)=lower("+sqlStr(role)+")", nil); err != nil {
				return err
			}
			if _, err := s.exec("INSERT INTO _overlite_memberships (member, roleof, admin_option) VALUES ("+
				sqlStr(mem)+", "+sqlStr(role)+", "+admin+")", nil); err != nil {
				return err
			}
		}
	}
	return nil
}

// canGrantMembership reports whether the current role may grant/revoke
// membership in role: a superuser/unmanaged role, or a holder of ADMIN OPTION.
func (s *session) canGrantMembership(role string) bool {
	return s.roleBypasses(s.currentRole) || s.hasAdminOption(role)
}

// roleGrant is the parsed form of a role-membership GRANT/REVOKE.
type roleGrant struct {
	roles     []string
	members   []string
	admin     bool // GRANT ... WITH ADMIN OPTION
	adminOnly bool // REVOKE ADMIN OPTION FOR ...
}

// parseRoleGrant splits "GRANT r1, r2 TO m1, m2 [WITH ADMIN OPTION]" or
// "REVOKE [ADMIN OPTION FOR] r1 FROM m1" into its parts.
func parseRoleGrant(sql string, revoke bool) (roleGrant, bool) {
	sep := "to"
	if revoke {
		sep = "from"
	}
	low := strings.ToLower(sql)
	sepIdx := indexWord(low, sep)
	if sepIdx < 0 {
		return roleGrant{}, false
	}
	rolesSpec := sql[firstWordEnd(sql):sepIdx]
	memSpec := sql[sepIdx+len(sep):]

	var rg roleGrant
	// GRANT ... WITH ADMIN OPTION
	if k := strings.Index(strings.ToLower(memSpec), "with admin"); k >= 0 {
		rg.admin = true
		memSpec = memSpec[:k]
	}
	// REVOKE ADMIN OPTION FOR <role> FROM ...
	if rs := strings.TrimSpace(rolesSpec); len(rs) >= 15 && strings.EqualFold(rs[:15], "admin option fo") {
		if i := strings.Index(strings.ToLower(rs), "for"); i >= 0 {
			rg.adminOnly = true
			rolesSpec = rs[i+len("for"):]
		}
	}
	for _, tail := range []string{" granted by", " cascade", " restrict"} {
		if k := strings.Index(strings.ToLower(memSpec), tail); k >= 0 {
			memSpec = memSpec[:k]
		}
	}
	memSpec = strings.TrimRight(memSpec, "; \t\n")
	rg.roles = splitGrantees(rolesSpec)
	rg.members = splitGrantees(memSpec)
	if len(rg.roles) == 0 || len(rg.members) == 0 {
		return roleGrant{}, false
	}
	return rg, true
}

// parseGrant extracts the privileges, tables, and grantees from a table
// GRANT/REVOKE. ok is false for forms it doesn't model (e.g. GRANT <role> TO,
// GRANT ... ON SCHEMA/SEQUENCE/DATABASE, GRANT ... ON ALL TABLES IN SCHEMA).
func parseGrant(sql string, revoke bool) (privs, tables, grantees []string, ok bool) {
	low := strings.ToLower(sql)
	on := indexWord(low, "on")
	if on < 0 {
		return nil, nil, nil, false
	}
	sep := "to"
	if revoke {
		sep = "from"
	}
	fromKw := indexWord(low, sep)
	if fromKw < 0 || fromKw < on {
		return nil, nil, nil, false
	}

	privSpec := strings.TrimSpace(sql[firstWordEnd(sql):on])
	objSpec := strings.TrimSpace(sql[on+2 : fromKw])
	granteeSpec := strings.TrimSpace(sql[fromKw+len(sep):])
	granteeSpec = strings.TrimRight(granteeSpec, "; \t\n")
	// Strip GRANT-option / cascade tails we don't model.
	for _, tail := range []string{" with grant option", " cascade", " restrict"} {
		if k := strings.Index(strings.ToLower(granteeSpec), tail); k >= 0 {
			granteeSpec = granteeSpec[:k]
		}
	}

	// Object must be a plain (optionally TABLE-qualified) table list.
	objSpec = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(objSpec), "TABLE"))
	objSpec = strings.TrimSpace(strings.TrimPrefix(objSpec, "table"))
	lowObj := strings.ToLower(objSpec)
	for _, kind := range []string{"schema", "sequence", "database", "function", "all tables", "all "} {
		if strings.HasPrefix(lowObj, kind) {
			return nil, nil, nil, false
		}
	}

	privs = splitPrivs(privSpec)
	tables = splitTables(objSpec)
	grantees = splitGrantees(granteeSpec)
	if len(privs) == 0 || len(tables) == 0 || len(grantees) == 0 {
		return nil, nil, nil, false
	}
	return privs, tables, grantees, true
}

// splitPrivs turns "SELECT, INSERT" / "ALL" / "ALL PRIVILEGES" into a list.
func splitPrivs(spec string) []string {
	l := strings.ToLower(strings.TrimSpace(spec))
	if l == "all" || l == "all privileges" || l == "" {
		return []string{"ALL"}
	}
	var out []string
	for _, p := range strings.Split(spec, ",") {
		p = strings.ToUpper(strings.TrimSpace(p))
		if i := strings.IndexByte(p, '('); i >= 0 { // column-list priv -> drop columns
			p = strings.TrimSpace(p[:i])
		}
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func splitTables(spec string) []string {
	var out []string
	for _, t := range strings.Split(spec, ",") {
		if name, ok := tableName(word{text: strings.TrimSpace(t)}); ok {
			out = append(out, name)
		}
	}
	return out
}

// splitGrantees returns role names, mapping GROUP/PUBLIC and quotes away.
func splitGrantees(spec string) []string {
	var out []string
	for _, g := range strings.Split(spec, ",") {
		g = strings.TrimSpace(g)
		g = strings.TrimPrefix(g, "GROUP ")
		g = strings.TrimPrefix(g, "group ")
		g = strings.TrimSpace(g)
		if strings.EqualFold(g, "public") {
			out = append(out, "public")
			continue
		}
		g = strings.Trim(g, `"`)
		if g != "" {
			out = append(out, g)
		}
	}
	return out
}

// recordOwnership keeps _overlite_owners in step with CREATE/DROP TABLE, run
// after the statement succeeds. Best effort: failures don't fail the statement.
func (s *session) recordOwnership(sql string) {
	f := strings.Fields(sql)
	if len(f) < 3 {
		return
	}
	switch {
	case strings.EqualFold(f[0], "create") && strings.EqualFold(f[1], "table"):
		if name := createdTableName(f[2:]); name != "" {
			_, _ = s.exec("INSERT OR REPLACE INTO _overlite_owners (tablename, owner) VALUES ("+
				sqlStr(name)+", "+sqlStr(s.currentRole)+")", nil)
		}
	case strings.EqualFold(f[0], "drop") && strings.EqualFold(f[1], "table"):
		if name := createdTableName(f[2:]); name != "" {
			_, _ = s.exec("DELETE FROM _overlite_owners WHERE lower(tablename)=lower("+sqlStr(name)+")", nil)
			_, _ = s.exec("DELETE FROM _overlite_grants WHERE lower(tablename)=lower("+sqlStr(name)+")", nil)
		}
	}
}

// createdTableName pulls the table name from the tokens after CREATE/DROP TABLE,
// skipping "IF NOT EXISTS" / "IF EXISTS", and returns the bare (unqualified) name.
func createdTableName(toks []string) string {
	for len(toks) > 0 {
		switch strings.ToLower(toks[0]) {
		case "if", "not", "exists":
			toks = toks[1:]
		default:
			raw := toks[0]
			if i := strings.IndexAny(raw, "(;"); i >= 0 {
				raw = raw[:i]
			}
			if name, ok := tableName(word{text: strings.TrimSpace(raw)}); ok {
				return name
			}
			return ""
		}
	}
	return ""
}

// indexWord returns the byte index of a whole-word keyword in a lower-cased
// string, or -1. Used on already-lower-cased input.
func indexWord(low, kw string) int {
	for i := 0; i+len(kw) <= len(low); {
		j := strings.Index(low[i:], kw)
		if j < 0 {
			return -1
		}
		p := i + j
		beforeOK := p == 0 || !isIdentPart(low[p-1])
		afterOK := p+len(kw) == len(low) || !isIdentPart(low[p+len(kw)])
		if beforeOK && afterOK {
			return p
		}
		i = p + len(kw)
	}
	return -1
}

// firstWordEnd returns the byte offset just past the first word of sql.
func firstWordEnd(sql string) int {
	i := 0
	for i < len(sql) && (sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n') {
		i++
	}
	for i < len(sql) && !(sql[i] == ' ' || sql[i] == '\t' || sql[i] == '\n') {
		i++
	}
	return i
}
