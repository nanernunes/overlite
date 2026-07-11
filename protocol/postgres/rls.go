package postgres

import (
	"fmt"
	"strings"

	"overlite/core"
)

// Row-level security. SQLite has none, so overlite records the flags/policies
// (see engine/rls.go) and injects each policy's USING/WITH CHECK expression:
//   - SELECT: RLS tables in FROM/JOIN are wrapped in a filtering subquery.
//   - UPDATE/DELETE: the USING expression is ANDed into the WHERE clause.
//   - INSERT: the WITH CHECK (or USING) expression validates every new row.
// A superuser, a BYPASSRLS role, an unmanaged role, or the table owner (unless
// FORCE) is not subject. With RLS on and no permissive policy, access is denied.

// isRLSDDL reports whether sql is an RLS DDL statement handled by tryRLSDDL.
func isRLSDDL(sql string) bool {
	if strings.EqualFold(firstWordUpper(sql), "ALTER") && strings.EqualFold(secondWordUpper(sql), "TABLE") &&
		strings.Contains(strings.ToLower(sql), "row level security") {
		return true
	}
	w, w2 := firstWordUpper(sql), secondWordUpper(sql)
	return w2 == "POLICY" && (w == "CREATE" || w == "DROP" || w == "ALTER")
}

// tryRLSDDL handles ALTER TABLE … ROW LEVEL SECURITY and CREATE/DROP/ALTER
// POLICY, which SQLite cannot run.
func (s *session) tryRLSDDL(sql string) (string, bool, error) {
	if !isRLSDDL(sql) {
		return "", false, nil
	}
	if strings.EqualFold(firstWordUpper(sql), "ALTER") && strings.EqualFold(secondWordUpper(sql), "TABLE") {
		return "ALTER TABLE", true, s.alterTableRLS(sql)
	}
	switch firstWordUpper(sql) {
	case "CREATE":
		return "CREATE POLICY", true, s.createPolicy(sql)
	case "DROP":
		return "DROP POLICY", true, s.dropPolicy(sql)
	default: // ALTER POLICY — accepted as a no-op
		return "ALTER POLICY", true, nil
	}
}

func (s *session) alterTableRLS(sql string) error {
	f := strings.Fields(sql)
	rest := f[2:] // after ALTER TABLE
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("syntax error in ALTER TABLE")
	}
	table := unquoteIdent(rest[0])
	if !s.roleBypasses(s.currentRole) && !s.ownsTable(table, s.effectiveRoles()) {
		return fmt.Errorf("must be owner of table %q", table)
	}
	low := strings.ToLower(sql)
	if _, err := s.exec("INSERT OR IGNORE INTO _overlite_rls (tablename, enabled, forced) VALUES ("+
		sqlStr(table)+", 0, 0)", nil); err != nil {
		return err
	}
	var set string
	switch {
	case strings.Contains(low, "disable"):
		set = "enabled = 0"
	case strings.Contains(low, "no force"):
		set = "forced = 0"
	case strings.Contains(low, "force"):
		set = "forced = 1"
	default: // enable
		set = "enabled = 1"
	}
	_, err := s.exec("UPDATE _overlite_rls SET "+set+" WHERE lower(tablename) = lower("+sqlStr(table)+")", nil)
	return err
}

type policyDef struct {
	name, table, command, roles, using, check string
	permissive                                string // "1" or "0"
}

func (s *session) createPolicy(sql string) error {
	p, err := parsePolicy(sql)
	if err != nil {
		return err
	}
	if !s.roleBypasses(s.currentRole) && !s.ownsTable(p.table, s.effectiveRoles()) {
		return fmt.Errorf("must be owner of table %q", p.table)
	}
	_, err = s.exec("INSERT INTO _overlite_policies"+
		" (polname, tablename, command, roles, permissive, using_expr, check_expr) VALUES ("+
		sqlStr(p.name)+", "+sqlStr(p.table)+", "+sqlStr(p.command)+", "+sqlStr(p.roles)+", "+
		p.permissive+", "+sqlStr(p.using)+", "+sqlStr(p.check)+")", nil)
	return err
}

func (s *session) dropPolicy(sql string) error {
	f := strings.Fields(sql)
	rest := f[2:] // after DROP POLICY
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		rest = rest[2:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("syntax error in DROP POLICY")
	}
	name := unquoteIdent(rest[0])
	table := ""
	for i, w := range rest {
		if strings.EqualFold(w, "on") && i+1 < len(rest) {
			table = unquoteIdent(strings.TrimRight(rest[i+1], ";"))
		}
	}
	_, err := s.exec("DELETE FROM _overlite_policies WHERE lower(polname) = lower("+sqlStr(name)+
		") AND lower(tablename) = lower("+sqlStr(table)+")", nil)
	return err
}

// parsePolicy parses CREATE POLICY name ON table [AS PERMISSIVE|RESTRICTIVE]
// [FOR cmd] [TO roles] [USING (expr)] [WITH CHECK (expr)].
func parsePolicy(sql string) (policyDef, error) {
	f := strings.Fields(sql)
	if len(f) < 5 || !strings.EqualFold(f[3], "on") {
		return policyDef{}, fmt.Errorf("syntax error in CREATE POLICY")
	}
	p := policyDef{
		name:       unquoteIdent(f[2]),
		table:      unquoteIdent(strings.TrimRight(f[4], ";")),
		command:    "ALL",
		permissive: "1",
	}
	low := strings.ToLower(sql)
	if indexWord(low, "restrictive") >= 0 {
		p.permissive = "0"
	}
	if i := indexWord(low, "for"); i >= 0 {
		p.command = strings.ToUpper(firstWordOf(sql[i+len("for"):]))
	}
	if i := indexWord(low, "using"); i >= 0 {
		if op := strings.IndexByte(sql[i:], '('); op >= 0 {
			inner, _ := balancedParen(sql, i+op)
			p.using = strings.TrimSpace(inner)
		}
	}
	if i := strings.Index(low, "with check"); i >= 0 {
		if op := strings.IndexByte(sql[i:], '('); op >= 0 {
			inner, _ := balancedParen(sql, i+op)
			p.check = strings.TrimSpace(inner)
		}
	}
	if i := indexWord(low, "to"); i >= 0 {
		rest := sql[i+len("to"):]
		end := len(rest)
		for _, kw := range []string{"using", "with check", "with", "for"} {
			if k := indexWord(strings.ToLower(rest), kw); k >= 0 && k < end {
				end = k
			}
		}
		p.roles = normalizeRoleList(rest[:end])
	}
	return p, nil
}

// --- enforcement ------------------------------------------------------------

// applyRLS rewrites a statement to enforce row-level security. INSERT is handled
// separately (tryRLSInsert) since it needs a runtime row check.
func (s *session) applyRLS(sql string) string {
	switch firstWordUpper(sql) {
	case "SELECT", "WITH", "TABLE", "VALUES":
		return s.wrapRLSSelect(sql)
	case "UPDATE":
		return s.injectRLSWhere(sql, "UPDATE")
	case "DELETE":
		return s.injectRLSWhere(sql, "DELETE")
	}
	return sql
}

// rlsFilter returns the combined USING filter for command on table, and whether
// the current role is subject to RLS there.
func (s *session) rlsFilter(table, command string) (string, bool) {
	rs, err := s.exec("SELECT enabled, forced FROM _overlite_rls WHERE lower(tablename) = lower("+
		sqlStr(table)+")", nil)
	if err != nil || len(rs.Rows) == 0 || !truthy(rs.Rows[0][0]) {
		return "", false
	}
	forced := truthy(rs.Rows[0][1])
	if s.rlsBypasses(table, forced) {
		return "", false
	}
	perm, restr := s.gatherPolicies(table, command, false)
	return combineRLS(perm, restr), true
}

// rlsCheckExpr returns the WITH CHECK filter for INSERT and whether subject.
func (s *session) rlsCheckExpr(table string) (string, bool) {
	rs, err := s.exec("SELECT enabled, forced FROM _overlite_rls WHERE lower(tablename) = lower("+
		sqlStr(table)+")", nil)
	if err != nil || len(rs.Rows) == 0 || !truthy(rs.Rows[0][0]) {
		return "", false
	}
	if s.rlsBypasses(table, truthy(rs.Rows[0][1])) {
		return "", false
	}
	perm, restr := s.gatherPolicies(table, "INSERT", true)
	return combineRLS(perm, restr), true
}

func (s *session) rlsBypasses(table string, forced bool) bool {
	if s.roleBypasses(s.currentRole) || s.hasBypassRLS(s.currentRole) {
		return true
	}
	if !forced && s.ownsTable(table, s.effectiveRoles()) {
		return true
	}
	return false
}

func (s *session) hasBypassRLS(role string) bool {
	rs, err := s.exec("SELECT 1 FROM _overlite_roles WHERE lower(rolname) = lower("+
		sqlStr(role)+") AND rolbypassrls <> 0 LIMIT 1", nil)
	return err == nil && len(rs.Rows) > 0
}

// gatherPolicies returns the permissive and restrictive expressions that apply
// to command for the current role, using WITH CHECK when forCheck (falling back
// to USING).
func (s *session) gatherPolicies(table, command string, forCheck bool) (perm, restr []string) {
	rs, err := s.exec("SELECT roles, permissive, using_expr, check_expr FROM _overlite_policies"+
		" WHERE lower(tablename) = lower("+sqlStr(table)+") AND upper(command) IN ('ALL', upper("+
		sqlStr(command)+"))", nil)
	if err != nil {
		return
	}
	roleSet := map[string]bool{"public": true}
	for _, r := range s.effectiveRoles() {
		roleSet[strings.ToLower(r)] = true
	}
	for _, row := range rs.Rows {
		if !policyRoleMatch(asString(row[0]), roleSet) {
			continue
		}
		expr := asString(row[2])
		if forCheck {
			if c := strings.TrimSpace(asString(row[3])); c != "" {
				expr = c
			}
		}
		if strings.TrimSpace(expr) == "" {
			expr = "1" // a policy with no relevant expression permits the row
		}
		if truthy(row[1]) {
			perm = append(perm, expr)
		} else {
			restr = append(restr, expr)
		}
	}
	return
}

func policyRoleMatch(polRoles string, roleSet map[string]bool) bool {
	polRoles = strings.TrimSpace(polRoles)
	if polRoles == "" {
		return true // TO PUBLIC / no TO clause
	}
	for _, r := range strings.Split(polRoles, ",") {
		if roleSet[strings.ToLower(strings.TrimSpace(r))] {
			return true
		}
	}
	return false
}

// combineRLS ORs the permissive expressions and ANDs the restrictive ones; no
// permissive policy means default-deny.
func combineRLS(perm, restr []string) string {
	var p string
	if len(perm) == 0 {
		p = "(0)"
	} else {
		parts := make([]string, len(perm))
		for i, e := range perm {
			parts[i] = "(" + e + ")"
		}
		p = "(" + strings.Join(parts, " OR ") + ")"
	}
	if len(restr) == 0 {
		return p
	}
	parts := make([]string, len(restr))
	for i, e := range restr {
		parts[i] = "(" + e + ")"
	}
	return p + " AND (" + strings.Join(parts, " AND ") + ")"
}

// wrapRLSSelect replaces every base-table reference in a FROM/JOIN clause that
// is under RLS with a filtering subquery.
func (s *session) wrapRLSSelect(sql string) string {
	toks := rlsTokens(sql)
	type repl struct {
		start, end int
		text       string
	}
	var reps []repl
	inFromList, expectTable := false, false
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		switch t.kind {
		case 'w':
			w := strings.ToLower(t.text)
			switch w {
			case "from":
				inFromList, expectTable = true, true
				continue
			case "join":
				expectTable = true
				continue
			case "only", "lateral":
				continue // modifiers before the table name
			case "where", "group", "having", "order", "limit", "window",
				"union", "except", "intersect", "on", "using", "returning", "set", "values":
				inFromList, expectTable = false, false
				continue
			}
			if expectTable {
				if bare := bareTableName(t.text); bare != "" {
					if expr, ok := s.rlsFilter(bare, "SELECT"); ok {
						r := "(SELECT * FROM " + t.text + " WHERE " + expr + ")"
						if !aliasFollows(toks, i) {
							r += " AS " + bareAlias(t.text)
						}
						reps = append(reps, repl{t.start, t.end, r})
					}
				}
				expectTable = false
			}
		case '(':
			expectTable = false // derived table; its inner FROM is scanned too
		case ',':
			if inFromList {
				expectTable = true
			}
		}
	}
	if len(reps) == 0 {
		return sql
	}
	var b strings.Builder
	prev := 0
	for _, r := range reps {
		b.WriteString(sql[prev:r.start])
		b.WriteString(r.text)
		prev = r.end
	}
	b.WriteString(sql[prev:])
	return b.String()
}

// injectRLSWhere ANDs the USING filter for the target table into an UPDATE or
// DELETE, before any RETURNING clause.
func (s *session) injectRLSWhere(sql, command string) string {
	kw := "update"
	if command == "DELETE" {
		kw = "from"
	}
	tps := tablesAfter(sql, []string{kw}, command)
	if len(tps) == 0 {
		return sql
	}
	expr, ok := s.rlsFilter(tps[0].table, command)
	if !ok {
		return sql
	}
	body := strings.TrimRight(sql, "; \t\n")
	semi := sql[len(body):]
	ret := ""
	if ri := topLevelKeyword(body, "returning"); ri >= 0 {
		ret, body = body[ri:], body[:ri]
	}
	if w := topLevelKeyword(body, "where"); w >= 0 {
		after := w + len("where")
		cond := strings.TrimSpace(body[after:])
		body = body[:after] + " ((" + expr + ") AND (" + cond + "))"
	} else {
		body = strings.TrimRight(body, " \t\n") + " WHERE (" + expr + ")"
	}
	if ret != "" {
		body += " " + strings.TrimSpace(ret)
	}
	return body + semi
}

// tryRLSInsert enforces WITH CHECK on INSERT (both VALUES and SELECT sources)
// for a subject role. It counts how many of the new rows satisfy the policy
// versus how many the source produces (binding the same params, so
// parameterized inserts are covered) and, if any row fails, rejects the
// statement before the real INSERT runs.
func (s *session) tryRLSInsert(sql string, params []core.Value) (bool, *core.ResultSet, error) {
	if firstWordUpper(sql) != "INSERT" {
		return false, nil, nil
	}
	target, cols, srcStart := parseInsert(sql)
	if target == "" || srcStart < 0 {
		return false, nil, nil
	}
	bare := bareTableName(target)
	expr, subject := s.rlsCheckExpr(bare)
	if !subject {
		return false, nil, nil
	}
	// The row source is everything after the target/column-list — VALUES (…) or
	// SELECT …, both valid as a CTE body — minus any trailing RETURNING.
	source := strings.TrimRight(sql[srcStart:], "; \t\n")
	if ri := topLevelKeyword(source, "returning"); ri >= 0 {
		source = source[:ri]
	}
	srcLow := strings.ToLower(strings.TrimSpace(source))
	// Forms we can't turn into a row-count check: enforce default-deny, else allow.
	if strings.Contains(strings.ToLower(sql), "on conflict") ||
		strings.HasPrefix(srcLow, "default") || strings.HasPrefix(srcLow, "overriding") {
		if expr == "(0)" {
			return true, nil, fmt.Errorf("new row violates row-level security policy for table %q", bare)
		}
		return false, nil, nil
	}
	if len(cols) == 0 {
		cols = s.tableColumns(bare)
	}
	if len(cols) == 0 {
		return false, nil, nil
	}
	collist := strings.Join(cols, ", ")
	cte := "WITH _rls_src (" + collist + ") AS (" + source + ") "

	// The VALUES may carry the same $N placeholders as the original INSERT, so
	// bind params to both counts.
	prov, err := s.exec(rewrite(cte+"SELECT count(*) FROM _rls_src"), params)
	if err != nil || len(prov.Rows) == 0 {
		return false, nil, nil // can't validate the VALUES shape; fall back to normal exec
	}
	pass, err := s.exec(rewrite(s.expandSessionUser(cte+"SELECT count(*) FROM _rls_src WHERE "+expr)), params)
	if err != nil || len(pass.Rows) == 0 {
		return false, nil, nil
	}
	if toInt(pass.Rows[0][0]) < toInt(prov.Rows[0][0]) {
		return true, nil, fmt.Errorf("new row violates row-level security policy for table %q", bare)
	}
	// All rows pass; let the normal INSERT path execute the statement as-is.
	return false, nil, nil
}

// parseInsert extracts the target table token, an explicit column list (if any),
// and the byte index where the row source (VALUES/SELECT/…) begins (-1 if none).
func parseInsert(sql string) (target string, cols []string, srcStart int) {
	srcStart = -1
	toks := rlsTokens(sql)
	for i := 0; i < len(toks); i++ {
		if toks[i].kind != 'w' || !strings.EqualFold(toks[i].text, "into") || i+1 >= len(toks) {
			continue
		}
		target = toks[i+1].text
		j := i + 2
		// Optional column list in parentheses before the source query.
		if j < len(toks) && toks[j].kind == '(' {
			depth := 0
			for ; j < len(toks); j++ {
				switch toks[j].kind {
				case '(':
					depth++
				case ')':
					depth--
				case 'w':
					if depth == 1 {
						cols = append(cols, toks[j].text)
					}
				}
				if depth == 0 {
					j++
					break
				}
			}
		}
		if j < len(toks) {
			srcStart = toks[j].start
		}
		break
	}
	return target, cols, srcStart
}

func (s *session) tableColumns(table string) []string {
	rs, err := s.exec("SELECT name FROM pragma_table_info("+sqlStr(table)+")", nil)
	if err != nil {
		return nil
	}
	var cols []string
	for _, row := range rs.Rows {
		if n := asString(row[0]); n != "" {
			cols = append(cols, n)
		}
	}
	return cols
}

// --- small helpers ----------------------------------------------------------

type sqlTok struct {
	text       string
	start, end int
	kind       byte // 'w' word/ident, '(' ')' ',' punctuation, 'o' other
}

func rlsTokens(sql string) []sqlTok {
	var out []sqlTok
	for i := 0; i < len(sql); {
		c := sql[i]
		switch {
		case c == '\'':
			i = endOfStringLiteral(sql, i)
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '(' || c == ')' || c == ',':
			out = append(out, sqlTok{string(c), i, i + 1, c})
			i++
		case c == '"' || isIdentStart(c):
			j := i
			for j < len(sql) {
				d := sql[j]
				if d == '"' {
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
			out = append(out, sqlTok{sql[i:j], i, j, 'w'})
			i = j
		default:
			out = append(out, sqlTok{string(c), i, i + 1, 'o'})
			i++
		}
	}
	return out
}

// rlsAliasStop holds keywords that cannot be a table alias (so a bare alias is
// injected instead).
var rlsAliasStop = map[string]bool{
	"on": true, "using": true, "where": true, "group": true, "order": true,
	"having": true, "limit": true, "join": true, "inner": true, "left": true,
	"right": true, "full": true, "cross": true, "natural": true, "union": true,
	"except": true, "intersect": true, "window": true, "returning": true,
	"set": true, "as": false, // handled explicitly
}

func aliasFollows(toks []sqlTok, i int) bool {
	if i+1 >= len(toks) || toks[i+1].kind != 'w' {
		return false
	}
	w := strings.ToLower(toks[i+1].text)
	if w == "as" {
		return true
	}
	return !rlsAliasStop[w]
}

func bareTableName(text string) string {
	t := bareAlias(text)
	low := strings.ToLower(t)
	if strings.HasPrefix(low, "pg_") || strings.HasPrefix(low, "sqlite_") ||
		strings.HasPrefix(low, "_overlite") || low == "information_schema" {
		return ""
	}
	return t
}

func bareAlias(text string) string {
	if i := strings.LastIndex(text, "."); i >= 0 {
		text = text[i+1:]
	}
	return strings.Trim(text, `"`)
}

// topLevelKeyword returns the byte index of a whole-word keyword at paren depth
// zero, outside string/quoted-identifier literals, or -1.
func topLevelKeyword(s, kw string) int {
	depth := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\'':
			i = endOfStringLiteral(s, i)
		case c == '"':
			i++
			for i < len(s) && s[i] != '"' {
				i++
			}
			if i < len(s) {
				i++
			}
		case c == '(':
			depth++
			i++
		case c == ')':
			if depth > 0 {
				depth--
			}
			i++
		case depth == 0 && isIdentStart(c):
			j := i
			for j < len(s) && isIdentPart(s[j]) {
				j++
			}
			if strings.EqualFold(s[i:j], kw) {
				return i
			}
			i = j
		default:
			i++
		}
	}
	return -1
}

func balancedParen(s string, open int) (string, int) {
	depth := 0
	for i := open; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = endOfStringLiteral(s, i) - 1
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return s[open+1 : i], i + 1
			}
		}
	}
	return s[open+1:], len(s)
}

func firstWordOf(s string) string {
	s = strings.TrimLeft(s, " \t\n")
	i := 0
	for i < len(s) && !(s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '(') {
		i++
	}
	return strings.TrimRight(s[:i], ";")
}

func normalizeRoleList(spec string) string {
	var out []string
	for _, r := range strings.Split(spec, ",") {
		r = strings.TrimSpace(strings.TrimRight(r, ";"))
		r = strings.TrimPrefix(strings.TrimPrefix(r, "GROUP "), "group ")
		r = strings.Trim(strings.TrimSpace(r), `"`)
		if r != "" {
			out = append(out, strings.ToLower(r))
		}
	}
	return strings.Join(out, ",")
}

func asString(v core.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

func toInt(v core.Value) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}
