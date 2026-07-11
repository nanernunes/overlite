package postgres

import (
	"fmt"
	"regexp"
	"strings"
)

// Enum types: CREATE/ALTER/DROP TYPE ... AS ENUM are intercepted here and
// recorded in the engine's internal enum tables; a column whose type is a known
// enum is rewritten to TEXT plus a CHECK constraint (native SQLite, so the file
// stays usable without overlite and membership is enforced). pg_type/pg_enum are
// populated from the same tables for introspection. Ordering/comparison uses the
// stored text, not the declared enum order.

var reAsEnum = regexp.MustCompile(`(?i)\bas\s+enum\b`)

// isTypeDDL reports whether sql is a CREATE/ALTER/DROP TYPE statement (used by
// the extended-query path to route it like other utility DDL).
func isTypeDDL(sql string) bool {
	fields := strings.Fields(sql)
	if len(fields) < 2 || !strings.EqualFold(fields[1], "type") {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "ALTER", "DROP":
		return true
	}
	return false
}

// tryTypeDDL handles CREATE/ALTER/DROP TYPE. It returns handled=true when it
// recognized the statement (mirrors tryRoleDDL / trySequenceDDL).
func (s *session) tryTypeDDL(sql string) (tag string, handled bool, err error) {
	fields := strings.Fields(sql)
	if len(fields) < 2 || !strings.EqualFold(fields[1], "type") {
		return "", false, nil
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		tag, err = s.createEnumType(sql, fields)
		return tag, true, err
	case "ALTER":
		tag, err = s.alterEnumType(sql, fields)
		return tag, true, err
	case "DROP":
		return "DROP TYPE", true, s.dropType(fields[2:])
	}
	return "", false, nil
}

func (s *session) createEnumType(sql string, fields []string) (string, error) {
	if len(fields) < 3 {
		return "CREATE TYPE", fmt.Errorf("syntax error in CREATE TYPE")
	}
	name := enumIdent(fields[2])
	loc := reAsEnum.FindStringIndex(sql)
	if loc == nil {
		// Non-enum CREATE TYPE (composite/range/base): accept as a no-op so dumps
		// and tools don't fail; we don't model these.
		return "CREATE TYPE", nil
	}
	open := strings.IndexByte(sql[loc[1]:], '(')
	if open < 0 {
		return "CREATE TYPE", fmt.Errorf("syntax error in CREATE TYPE ... AS ENUM")
	}
	args, _, ok := readParenArgs(sql, loc[1]+open)
	if !ok {
		return "CREATE TYPE", fmt.Errorf("unterminated ENUM label list")
	}
	if _, err := s.exec("INSERT INTO _overlite_enum_types (typname) VALUES ("+sqlStr(name)+")", nil); err != nil {
		return "CREATE TYPE", err
	}
	for i, label := range parseEnumLabels(args) {
		if _, err := s.exec("INSERT INTO _overlite_enums (typname, label, sortorder) VALUES ("+
			sqlStr(name)+", "+sqlStr(label)+", "+i64(int64(i+1))+")", nil); err != nil {
			return "CREATE TYPE", err
		}
	}
	return "CREATE TYPE", nil
}

func (s *session) alterEnumType(sql string, fields []string) (string, error) {
	if len(fields) < 4 {
		return "ALTER TYPE", fmt.Errorf("syntax error in ALTER TYPE")
	}
	name := enumIdent(fields[2])
	rest := fields[3:]

	// ALTER TYPE x RENAME TO y
	if len(rest) >= 3 && strings.EqualFold(rest[0], "rename") && strings.EqualFold(rest[1], "to") {
		newName := enumIdent(rest[2])
		if _, err := s.exec("UPDATE _overlite_enum_types SET typname = "+sqlStr(newName)+
			" WHERE typname = "+sqlStr(name), nil); err != nil {
			return "ALTER TYPE", err
		}
		_, err := s.exec("UPDATE _overlite_enums SET typname = "+sqlStr(newName)+
			" WHERE typname = "+sqlStr(name), nil)
		return "ALTER TYPE", err
	}

	// ALTER TYPE x RENAME VALUE 'a' TO 'b'
	if len(rest) >= 2 && strings.EqualFold(rest[0], "rename") && strings.EqualFold(rest[1], "value") {
		qs := quotedStrings(sql[reAfterWord(sql, "value"):])
		if len(qs) < 2 {
			return "ALTER TYPE", fmt.Errorf("syntax error in ALTER TYPE ... RENAME VALUE")
		}
		_, err := s.exec("UPDATE _overlite_enums SET label = "+sqlStr(qs[1])+
			" WHERE typname = "+sqlStr(name)+" AND label = "+sqlStr(qs[0]), nil)
		return "ALTER TYPE", err
	}

	// ALTER TYPE x ADD VALUE [IF NOT EXISTS] 'v' [BEFORE|AFTER 'other']
	if len(rest) >= 2 && strings.EqualFold(rest[0], "add") && strings.EqualFold(rest[1], "value") {
		return "ALTER TYPE", s.addEnumValue(sql, name, rest[2:])
	}
	return "ALTER TYPE", nil // accept unrecognized ALTER TYPE as a no-op
}

func (s *session) addEnumValue(sql, name string, tokens []string) error {
	if !s.enumTypeExists(name) {
		return fmt.Errorf("type %q does not exist", name)
	}
	qs := quotedStrings(sql[reAfterWord(sql, "value"):])
	if len(qs) == 0 {
		return fmt.Errorf("syntax error in ALTER TYPE ... ADD VALUE")
	}
	label := qs[0]
	ifNotExists := len(tokens) >= 3 && strings.EqualFold(tokens[0], "if") &&
		strings.EqualFold(tokens[1], "not") && strings.EqualFold(tokens[2], "exists")
	if s.enumLabelExists(name, label) {
		if ifNotExists {
			return nil
		}
		return fmt.Errorf("label %q already exists in type %q", label, name)
	}

	order := "(SELECT COALESCE(MAX(sortorder), 0) + 1 FROM _overlite_enums WHERE typname = " + sqlStr(name) + ")"
	if pos, anchor := enumPosition(tokens, qs); pos != "" && anchor != "" {
		delta := "- 0.5"
		if pos == "after" {
			delta = "+ 0.5"
		}
		order = "(SELECT sortorder " + delta + " FROM _overlite_enums WHERE typname = " +
			sqlStr(name) + " AND label = " + sqlStr(anchor) + ")"
	}
	_, err := s.exec("INSERT INTO _overlite_enums (typname, label, sortorder) VALUES ("+
		sqlStr(name)+", "+sqlStr(label)+", "+order+")", nil)
	return err
}

func (s *session) dropType(rest []string) error {
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		rest = rest[2:]
	}
	for _, name := range strings.Split(strings.Join(rest, " "), ",") {
		name = enumIdent(strings.TrimSpace(strings.TrimRight(name, ";")))
		if name == "" || strings.EqualFold(name, "cascade") || strings.EqualFold(name, "restrict") {
			continue
		}
		if _, err := s.exec("DELETE FROM _overlite_enum_types WHERE typname = "+sqlStr(name), nil); err != nil {
			return err
		}
		if _, err := s.exec("DELETE FROM _overlite_enums WHERE typname = "+sqlStr(name), nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *session) enumTypeExists(name string) bool {
	rs, err := s.exec("SELECT 1 FROM _overlite_enum_types WHERE typname = "+sqlStr(name), nil)
	return err == nil && len(rs.Rows) > 0
}

func (s *session) enumLabelExists(name, label string) bool {
	rs, err := s.exec("SELECT 1 FROM _overlite_enums WHERE typname = "+sqlStr(name)+
		" AND label = "+sqlStr(label), nil)
	return err == nil && len(rs.Rows) > 0
}

// --- enum column rewriting ----------------------------------------------------

// expandEnums rewrites enum columns in a CREATE TABLE to TEXT + CHECK. It runs
// before the dialect rewrite (like expandSequences), so the labels it emits are
// protected by the string-literal-aware rewrite.
func (s *session) expandEnums(sql string) (string, error) {
	if firstWordUpper(sql) != "CREATE" || !containsWordFold(sql, "table") {
		return sql, nil
	}
	enums, err := s.loadEnums()
	if err != nil || len(enums) == 0 {
		return sql, err
	}
	return rewriteEnumColumns(sql, enums), nil
}

func (s *session) loadEnums() (map[string][]string, error) {
	rs, err := s.exec("SELECT typname, label FROM _overlite_enums ORDER BY typname, sortorder", nil)
	if err != nil {
		return nil, err
	}
	m := map[string][]string{}
	for _, row := range rs.Rows {
		if len(row) < 2 {
			continue
		}
		key := strings.ToLower(fmt.Sprint(row[0]))
		m[key] = append(m[key], fmt.Sprint(row[1]))
	}
	return m, nil
}

// rewriteEnumColumns replaces each column whose declared type is a known enum
// with "col text CHECK (col IN ('a','b',...))".
func rewriteEnumColumns(sql string, enums map[string][]string) string {
	open := indexTopLevelByte(sql, '(')
	if open < 0 {
		return sql
	}
	inner, end, ok := readParenArgs(sql, open)
	if !ok {
		return sql
	}
	parts := splitTopLevel(inner)
	changed := false
	for i, p := range parts {
		np := rewriteEnumColumnDef(p, enums)
		if np != p {
			parts[i] = np
			changed = true
		}
	}
	if !changed {
		return sql
	}
	return sql[:open] + "(" + strings.Join(parts, ",") + ")" + sql[end:]
}

func rewriteEnumColumnDef(def string, enums map[string][]string) string {
	trimmed := strings.TrimLeft(def, " \t\r\n")
	lead := def[:len(def)-len(trimmed)]
	name, afterName := firstToken(trimmed)
	if name == "" {
		return def
	}
	typTrimmed := strings.TrimLeft(afterName, " \t\r\n")
	typ, afterType := firstToken(typTrimmed)
	labels, ok := enums[strings.ToLower(strings.Trim(typ, `"`))]
	if !ok {
		return def
	}
	quoted := make([]string, len(labels))
	for i, l := range labels {
		quoted[i] = sqlStr(l)
	}
	return lead + name + " text CHECK (" + name + " IN (" + strings.Join(quoted, ", ") + "))" + afterType
}

// --- parsing helpers ----------------------------------------------------------

// enumIdent extracts a bare type name from a token: unquote and drop any schema
// qualifier (public.mood -> mood).
func enumIdent(tok string) string {
	t := unquoteIdent(strings.TrimSpace(tok))
	if i := strings.LastIndexByte(t, '.'); i >= 0 {
		t = t[i+1:]
	}
	return strings.Trim(t, `"`)
}

// parseEnumLabels splits a quoted, comma-separated label list into its values.
func parseEnumLabels(args string) []string {
	var out []string
	for _, p := range splitTopLevel(args) {
		p = strings.TrimSpace(p)
		p = strings.TrimSuffix(strings.TrimPrefix(p, "'"), "'")
		p = strings.ReplaceAll(p, "''", "'")
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// quotedStrings returns every single-quoted literal's content in s (in order),
// with doubled quotes unescaped.
func quotedStrings(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		j := endOfStringLiteral(s, i)
		body := s[i+1 : j-1]
		out = append(out, strings.ReplaceAll(body, "''", "'"))
		i = j - 1
	}
	return out
}

// enumPosition reads a BEFORE/AFTER anchor from an ADD VALUE clause. qs are the
// quoted strings in order (qs[0] is the new label); the anchor is qs[1].
func enumPosition(tokens, qs []string) (pos, anchor string) {
	for _, t := range tokens {
		l := strings.ToLower(t)
		if l == "before" || l == "after" {
			if len(qs) >= 2 {
				return l, qs[1]
			}
		}
	}
	return "", ""
}

// firstToken returns the first identifier token (bare or "quoted") of s and the
// remainder. It returns "" when s does not start with an identifier.
func firstToken(s string) (tok, rest string) {
	if s == "" {
		return "", ""
	}
	if s[0] == '"' {
		j := 1
		for j < len(s) {
			if s[j] == '"' {
				j++
				break
			}
			j++
		}
		return s[:j], s[j:]
	}
	j := 0
	for j < len(s) && isIdentPart(s[j]) {
		j++
	}
	if j == 0 {
		return "", s
	}
	return s[:j], s[j:]
}

// indexTopLevelByte finds the first b that is outside any string literal.
func indexTopLevelByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			i = endOfStringLiteral(s, i) - 1
			continue
		}
		if s[i] == b {
			return i
		}
	}
	return -1
}

func containsWordFold(sql, word string) bool {
	low := strings.ToLower(sql)
	w := strings.ToLower(word)
	for from := 0; ; {
		i := strings.Index(low[from:], w)
		if i < 0 {
			return false
		}
		i += from
		before := i == 0 || !isIdentPart(low[i-1])
		after := i+len(w) >= len(low) || !isIdentPart(low[i+len(w)])
		if before && after {
			return true
		}
		from = i + len(w)
	}
}

// reAfterWord returns the index just past the first whole-word occurrence of
// word (case-insensitive) in sql, or 0 if absent.
func reAfterWord(sql, word string) int {
	low := strings.ToLower(sql)
	w := strings.ToLower(word)
	for from := 0; ; {
		i := strings.Index(low[from:], w)
		if i < 0 {
			return 0
		}
		i += from
		before := i == 0 || !isIdentPart(low[i-1])
		after := i+len(w) >= len(low) || !isIdentPart(low[i+len(w)])
		if before && after {
			return i + len(w)
		}
		from = i + len(w)
	}
}
