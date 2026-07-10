package postgres

import (
	"regexp"
	"strings"
)

// rewrite translates the handful of Postgres-isms we support into equivalent
// SQLite. It is intentionally small and syntactic; a full SQL transpiler is a
// non-goal. Each rule is independently tested in dialect_test.go.
func rewrite(sql string) string {
	// Strip the pg_catalog. qualifier first, so schema-qualified type names in
	// casts (e.g. "x::pg_catalog.regtype") reduce to a bare, castable type.
	sql = rewritePgCatalogPrefix(sql)
	sql = rewritePublicPrefix(sql)
	sql = rewriteSerial(sql)
	sql = rewriteNow(sql)
	sql = rewriteExtract(sql)
	sql = rewriteJSONFuncs(sql)
	sql = rewriteJSONPath(sql)
	sql = rewriteEscapeStrings(sql)
	sql = rewriteNiladicFuncs(sql)
	sql = rewriteInformationSchema(sql)
	sql = rewriteOperatorSyntax(sql) // unwrap OPERATOR(=)/OPERATOR(~) before ANY/regex handling
	sql = rewriteArraySubquery(sql)
	sql = rewriteAnyArray(sql)
	sql = rewriteAnyOperator(sql)
	sql = rewriteStringAgg(sql)
	sql = rewriteGenerateSeries(sql)
	sql = rewriteCasts(sql)
	sql = rewriteArraySubscript(sql)
	sql = rewriteObjectDefs(sql)
	sql = rewriteCollate(sql)
	sql = rewriteTrimFrom(sql)
	sql = rewriteMatchOperators(sql)
	return sql
}

// rewriteTrimFrom converts SQL-standard trim syntax
// "trim([leading|trailing|both] chars from expr)" into SQLite's function form
// (ltrim/rtrim/trim(expr, chars)). psql uses it in its \d rule query.
func rewriteTrimFrom(sql string) string {
	fns := map[string]string{"leading": "ltrim", "trailing": "rtrim", "both": "trim"}
	for {
		m := findTrimFrom(sql)
		if !m.ok {
			return sql
		}
		repl := fns[m.kw] + "(" + sql[m.exprStart:m.exprEnd] + ", " + m.chars + ")"
		sql = sql[:m.start] + repl + sql[m.callEnd:]
	}
}

type trimMatch struct {
	ok                 bool
	start, callEnd     int // span of the whole trim(...) call
	exprStart, exprEnd int
	kw, chars          string
}

func findTrimFrom(sql string) trimMatch {
	low := strings.ToLower(sql)
	for from := 0; ; {
		i := strings.Index(low[from:], "trim(")
		if i < 0 {
			return trimMatch{}
		}
		i += from
		from = i + 5

		p := skipSpaces(sql, i+5)
		kw := readTrimKeyword(low, p)
		if kw == "" {
			continue // ordinary trim(...)
		}
		p = skipSpaces(sql, p+len(kw))
		cs, ce := operandAfter(sql, p)
		if cs == ce {
			continue
		}
		p = skipSpaces(sql, ce)
		if !hasWordAt(low, p, "from") {
			continue
		}
		p = skipSpaces(sql, p+len("from"))

		// Consume expr up to the ')' that closes this trim( call.
		depth, j := 1, p
		for j < len(sql) && depth > 0 {
			switch sql[j] {
			case '(':
				depth++
			case ')':
				depth--
			case '\'':
				for j++; j < len(sql) && sql[j] != '\''; j++ {
				}
			}
			if depth == 0 {
				break
			}
			j++
		}
		if depth != 0 {
			continue // unbalanced
		}
		exprEnd := j
		for exprEnd > p && sql[exprEnd-1] == ' ' {
			exprEnd--
		}
		return trimMatch{ok: true, start: i, callEnd: j + 1, exprStart: p, exprEnd: exprEnd, kw: kw, chars: sql[cs:ce]}
	}
}

func readTrimKeyword(low string, p int) string {
	for _, kw := range []string{"leading", "trailing", "both"} {
		if hasWordAt(low, p, kw) {
			return kw
		}
	}
	return ""
}

func hasWordAt(low string, p int, word string) bool {
	if p+len(word) > len(low) || low[p:p+len(word)] != word {
		return false
	}
	n := p + len(word)
	return n >= len(low) || low[n] == ' ' || low[n] == '\t' || low[n] == '\n' || low[n] == '(' || low[n] == '\''
}

func skipSpaces(sql string, p int) int {
	for p < len(sql) && (sql[p] == ' ' || sql[p] == '\t' || sql[p] == '\n' || sql[p] == '\r') {
		p++
	}
	return p
}

// rewriteCasts turns every Postgres "operand::type" cast into SQLite's
// CAST(operand AS type). A hand-written scan (rather than a regex) is used so
// the operand can be a balanced parenthesized group, a quoted string, or a
// chained cast — none of which a regex handles cleanly.
func rewriteCasts(sql string) string {
	for {
		i := indexCast(sql)
		if i < 0 {
			return sql
		}
		ts := i + 2
		for ts < len(sql) && sql[ts] == ' ' {
			ts++
		}
		te := ts
		for te < len(sql) && isTypeChar(sql[te]) {
			te++
		}
		typeEnd := te
		// Drop array-type suffixes (int2[], text[][]); SQLite has no arrays and
		// these appear only in catalog queries that return no rows here.
		for strings.HasPrefix(sql[te:], "[]") {
			te += 2
		}
		os, oe := operandBefore(sql, i)
		if typeEnd == ts || os == oe {
			return sql // no type name or no operand: leave it, avoid looping
		}
		sql = sql[:os] + castExpr(sql[os:oe], sql[ts:typeEnd]) + sql[te:]
	}
}

// castExpr renders "operand::type". Postgres OID-alias types (regclass, ...)
// become a name lookup against the catalog so oids display as names, as psql
// expects; everything else becomes a plain SQLite CAST.
func castExpr(operand, typeName string) string {
	// A reg* cast on a numeric literal is a comparison seed (regclass is an oid
	// under the hood), so keep it numeric. On anything else it's for display,
	// so resolve the oid to its catalog name.
	regLookup := func(col, table string) string {
		// A quoted numeric oid ('16384'::regclass) must become a bare integer so
		// it compares equal to integer oid columns (SQLite won't equate 1 = '1').
		if isNumericLiteral(operand) {
			return strings.Trim(strings.TrimSpace(operand), "'")
		}
		return "(SELECT " + col + " FROM " + table + " WHERE oid = " + operand + ")"
	}
	switch strings.ToLower(strings.TrimSpace(typeName)) {
	case "regclass":
		return regLookup("relname", "pg_class")
	case "regnamespace":
		return regLookup("nspname", "pg_namespace")
	case "regtype":
		return regLookup("typname", "pg_type")
	case "regrole":
		return regLookup("rolname", "pg_roles")
	case "regproc", "regprocedure":
		return regLookup("proname", "pg_proc")
	default:
		return "CAST(" + operand + " AS " + typeName + ")"
	}
}

// isNumericLiteral reports whether s is an integer literal, optionally quoted
// (e.g. 16384 or '16384').
func isNumericLiteral(s string) bool {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "'"), "'")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// indexCast returns the index of the first "::" that is not inside a string
// literal, or -1.
func indexCast(sql string) int {
	inStr := false
	for i := 0; i+1 < len(sql); i++ {
		switch sql[i] {
		case '\'':
			inStr = !inStr
		case ':':
			if !inStr && sql[i+1] == ':' {
				return i
			}
		}
	}
	return -1
}

// operandBefore returns the [start,end) span of the operand ending just before
// the "::" at castIdx.
func operandBefore(sql string, castIdx int) (int, int) {
	end := castIdx
	for end > 0 && sql[end-1] == ' ' {
		end--
	}
	if end == 0 {
		return end, end
	}
	switch sql[end-1] {
	case ')':
		depth, j := 0, end-1
		for j >= 0 {
			if sql[j] == ')' {
				depth++
			} else if sql[j] == '(' {
				if depth--; depth == 0 {
					break
				}
			}
			j--
		}
		// Include a preceding function name, so CAST(x)::t keeps the CAST.
		for j > 0 && isOperandChar(sql[j-1]) {
			j--
		}
		return j, end
	case '\'':
		j := end - 2
		for j >= 0 && sql[j] != '\'' {
			j--
		}
		return j, end
	default:
		j := end
		for j > 0 && isOperandChar(sql[j-1]) {
			j--
		}
		return j, end
	}
}

func isTypeChar(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

func isOperandChar(b byte) bool {
	return b == '_' || b == '$' || b == '.' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// SERIAL/BIGSERIAL/SMALLSERIAL are Postgres auto-increment integers. In SQLite,
// an INTEGER PRIMARY KEY is the rowid and auto-increments, so we map the common
// "SERIAL PRIMARY KEY" to it (with AUTOINCREMENT to avoid rowid reuse, matching
// a sequence). A bare SERIAL becomes INTEGER (auto-increment only applies when
// it is the primary key).
var (
	reSerialPK = regexp.MustCompile(`(?i)\b(?:big|small)?serial\s+primary\s+key\b`)
	reSerial   = regexp.MustCompile(`(?i)\b(?:big|small)?serial\b`)
)

func rewriteSerial(sql string) string {
	// Only in DDL, so a column literally named "serial" in a query is safe.
	if h := firstWordUpper(sql); h != "CREATE" && h != "ALTER" {
		return sql
	}
	sql = reSerialPK.ReplaceAllString(sql, "INTEGER PRIMARY KEY AUTOINCREMENT")
	return reSerial.ReplaceAllString(sql, "INTEGER")
}

// reNowFuncs maps the Postgres "current timestamp" niladic functions onto
// SQLite's datetime('now'). (CURRENT_TIMESTAMP/CURRENT_DATE/CURRENT_TIME are
// already SQLite keywords and pass through untouched.)
var reNowFuncs = regexp.MustCompile(`(?i)\b(?:now|transaction_timestamp|statement_timestamp|clock_timestamp)\s*\(\s*\)`)

func rewriteNow(sql string) string {
	return reNowFuncs.ReplaceAllString(sql, "datetime('now')")
}

// rewriteExtract turns "extract(field FROM ts)" into the date_part('field', ts)
// call form, which the engine implements. SQLite has no EXTRACT.
func rewriteExtract(sql string) string {
	low := strings.ToLower(sql)
	for from := 0; ; {
		i := strings.Index(low[from:], "extract")
		if i < 0 {
			return sql
		}
		i += from
		from = i + len("extract")
		if i > 0 && isWordByte(low[i-1]) {
			continue // part of a longer identifier
		}
		p := skipSpaces(sql, i+len("extract"))
		if p >= len(sql) || sql[p] != '(' {
			continue
		}
		p = skipSpaces(sql, p+1)
		fieldStart := p
		for p < len(sql) && (isWordByte(sql[p])) {
			p++
		}
		field := sql[fieldStart:p]
		q := skipSpaces(sql, p)
		if !hasWordAt(low, q, "from") {
			continue
		}
		exprStart := skipSpaces(sql, q+len("from"))
		depth, j := 1, exprStart
		for j < len(sql) && depth > 0 {
			switch sql[j] {
			case '(':
				depth++
			case ')':
				depth--
			case '\'':
				for j++; j < len(sql) && sql[j] != '\''; j++ {
				}
			}
			if depth == 0 {
				break
			}
			j++
		}
		if depth != 0 {
			continue
		}
		exprEnd := j
		for exprEnd > exprStart && sql[exprEnd-1] == ' ' {
			exprEnd--
		}
		repl := "date_part('" + strings.ToLower(field) + "', " + sql[exprStart:exprEnd] + ")"
		sql = sql[:i] + repl + sql[j+1:]
		low = strings.ToLower(sql)
		from = i + len(repl)
	}
}

// jsonFuncMap maps Postgres JSON/JSONB builder/aggregate functions onto their
// SQLite json1 equivalents. The -> and ->> operators, plus json_extract/
// json_object/json_array/json_type/json_set/json_insert, are identical in
// SQLite and pass through untouched.
var jsonFuncMap = map[string]string{
	"json_build_object": "json_object", "jsonb_build_object": "json_object",
	"json_build_array": "json_array", "jsonb_build_array": "json_array",
	"json_object_agg": "json_group_object", "jsonb_object_agg": "json_group_object",
	"json_agg": "json_group_array", "jsonb_agg": "json_group_array",
	"json_typeof": "json_type", "jsonb_typeof": "json_type",
	"jsonb_array_length": "json_array_length",
	"to_json": "json_quote", "to_jsonb": "json_quote",
	"jsonb_pretty": "json", "jsonb_set": "json_set", "jsonb_insert": "json_insert",
}

var reJSONFunc = regexp.MustCompile(`(?i)\b(json_build_object|jsonb_build_object|json_build_array|jsonb_build_array|json_object_agg|jsonb_object_agg|json_agg|jsonb_agg|json_typeof|jsonb_typeof|jsonb_array_length|to_jsonb|to_json|jsonb_pretty|jsonb_set|jsonb_insert)\s*\(`)

func rewriteJSONFuncs(sql string) string {
	return reJSONFunc.ReplaceAllStringFunc(sql, func(m string) string {
		name := strings.ToLower(strings.TrimSpace(m[:strings.IndexByte(m, '(')]))
		if repl, ok := jsonFuncMap[name]; ok {
			return repl + "("
		}
		return m
	})
}

// rewriteJSONPath turns the Postgres path operators "expr #> '{a,b}'" and
// "#>>" into json_extract(expr, '$.a.b'). (-> and ->> are native to SQLite.)
func rewriteJSONPath(sql string) string {
	for {
		idx, op := indexHashArrow(sql)
		if idx < 0 {
			return sql
		}
		ls, le := operandBefore(sql, idx)
		rs := skipSpaces(sql, idx+len(op))
		if ls == le || rs >= len(sql) || sql[rs] != '\'' {
			return sql // unexpected shape; bail rather than loop
		}
		re := rs + 1
		for re < len(sql) && sql[re] != '\'' {
			re++
		}
		if re >= len(sql) {
			return sql
		}
		repl := "json_extract(" + sql[ls:le] + ", '" + hashPathToJSONPath(sql[rs+1:re]) + "')"
		sql = sql[:ls] + repl + sql[re+1:]
	}
}

// indexHashArrow finds the first #> or #>> operator outside a string literal.
func indexHashArrow(sql string) (int, string) {
	inStr := false
	for i := 0; i+1 < len(sql); i++ {
		switch sql[i] {
		case '\'':
			inStr = !inStr
		case '#':
			if !inStr && sql[i+1] == '>' {
				if i+2 < len(sql) && sql[i+2] == '>' {
					return i, "#>>"
				}
				return i, "#>"
			}
		}
	}
	return -1, ""
}

// hashPathToJSONPath converts a Postgres text-array path "{a,0,b}" to a SQLite
// JSON path "$.a[0].b".
func hashPathToJSONPath(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimSuffix(strings.TrimPrefix(p, "{"), "}")
	var b strings.Builder
	b.WriteByte('$')
	for _, part := range strings.Split(p, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if isAllDigits(part) {
			b.WriteString("[" + part + "]")
		} else {
			b.WriteString("." + part)
		}
	}
	return b.String()
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

// reEscapeString matches the Postgres escape-string prefix (E'...'), which
// SQLite lacks. Dropping the E leaves a normal string literal — good enough
// for the catalog queries that use it (e.g. E'\n' separators).
var reEscapeString = regexp.MustCompile(`(?i)\bE'`)

func rewriteEscapeStrings(sql string) string {
	return reEscapeString.ReplaceAllString(sql, "'")
}

// reNiladic matches SQL niladic special functions that Postgres allows without
// parentheses but SQLite requires as calls.
var reNiladic = regexp.MustCompile(`(?i)\b(current_user|session_user|current_role|current_catalog|current_schema|current_database)\b`)

// rewriteNiladicFuncs appends "()" to a bare niladic function so SQLite treats
// it as a call, skipping matches that are quoted, qualified, or already called.
func rewriteNiladicFuncs(sql string) string {
	var b strings.Builder
	last := 0
	for _, loc := range reNiladic.FindAllStringIndex(sql, -1) {
		b.WriteString(sql[last:loc[1]])
		last = loc[1]
		if loc[0] > 0 {
			if p := sql[loc[0]-1]; p == '"' || p == '\'' || p == '.' {
				continue // quoted identifier, string, or qualified name
			}
		}
		j := loc[1]
		for j < len(sql) && sql[j] == ' ' {
			j++
		}
		if j < len(sql) && sql[j] == '(' {
			continue // already a call
		}
		b.WriteString("()")
	}
	b.WriteString(sql[last:])
	return b.String()
}

// reInfoSchema matches references to the emulated information_schema so they
// resolve to the quoted, dotted view names the engine materializes.
var reInfoSchema = regexp.MustCompile(`(?i)\binformation_schema\.(tables|columns)\b`)

func rewriteInformationSchema(sql string) string {
	return reInfoSchema.ReplaceAllStringFunc(sql, func(m string) string {
		return `"` + strings.ToLower(m) + `"`
	})
}

// reArraySubquery matches Postgres' ARRAY(subquery) constructor, which SQLite
// lacks. We drop the ARRAY keyword, leaving a plain scalar subquery — enough to
// parse (these appear in catalog queries that return no rows here).
var reArraySubquery = regexp.MustCompile(`(?i)\barray\s*\(\s*(select\b)`)

func rewriteArraySubquery(sql string) string {
	return reArraySubquery.ReplaceAllString(sql, "(${1}")
}

// reAnyArray matches "= ANY (ARRAY[...])" / "<> ALL (ARRAY[...])", the array
// membership tests psql and pg_dump use, mapping them to SQLite IN / NOT IN.
var (
	reAnyArray = regexp.MustCompile(`(?is)\s*=\s*any\s*\(\s*array\s*\[([^\]]*)\]\s*\)`)
	reAllArray = regexp.MustCompile(`(?is)\s*(?:<>|!=)\s*all\s*\(\s*array\s*\[([^\]]*)\]\s*\)`)
)

func rewriteAnyArray(sql string) string {
	sql = reAnyArray.ReplaceAllString(sql, " IN ($1)")
	return reAllArray.ReplaceAllString(sql, " NOT IN ($1)")
}

// reAnyOperator matches "= ANY (subquery)". SQLite has no ANY; IN is close
// enough for the (non-executing) catalog queries that use it.
var reAnyOperator = regexp.MustCompile(`(?i)\s*=\s*any\s*\(`)

func rewriteAnyOperator(sql string) string {
	return reAnyOperator.ReplaceAllString(sql, " IN (")
}

// reArraySubscript matches a Postgres array subscript directly attached to an
// identifier (e.g. prattrs[s]). SQLite has no array subscripting; these appear
// only in catalog queries that never evaluate here, so we collapse them to
// NULL. Requiring no space before "[" avoids touching SQLite's [ident] quoting.
var reArraySubscript = regexp.MustCompile(`[A-Za-z_]\w*\[[^\]]*\]`)

func rewriteArraySubscript(sql string) string {
	return reArraySubscript.ReplaceAllString(sql, "NULL")
}

// reStringAgg maps Postgres string_agg() onto SQLite's group_concat().
var reStringAgg = regexp.MustCompile(`(?i)\bstring_agg\s*\(`)

func rewriteStringAgg(sql string) string {
	return reStringAgg.ReplaceAllString(sql, "group_concat(")
}

// rewriteGenerateSeries replaces generate_series(...) — a set-returning
// function SQLite's build lacks — with an empty single-column relation. It only
// appears in catalog queries that yield no rows here, so emptiness is correct.
func rewriteGenerateSeries(sql string) string {
	return replaceCall(sql, "generate_series", "(SELECT NULL AS value WHERE 0)")
}

// replaceCall replaces every call name(...) — with balanced parentheses — by
// repl. Matching is case-insensitive and respects word boundaries.
func replaceCall(sql, name, repl string) string {
	low := strings.ToLower(sql)
	lname := strings.ToLower(name)
	for from := 0; ; {
		i := indexWordCall(low, lname, from)
		if i < 0 {
			return sql
		}
		open := i + len(name)
		for open < len(sql) && sql[open] == ' ' {
			open++
		}
		depth, j := 0, open
		for j < len(sql) {
			if sql[j] == '(' {
				depth++
			} else if sql[j] == ')' {
				if depth--; depth == 0 {
					j++
					break
				}
			}
			j++
		}
		if depth != 0 {
			return sql // unbalanced; bail
		}
		sql = sql[:i] + repl + sql[j:]
		low = strings.ToLower(sql)
		from = i + len(repl)
	}
}

// rewriteObjectDefs turns pg_get_indexdef()/pg_get_constraintdef() calls into
// correlated subqueries that reconstruct the definition from our catalog views
// (ov_cols/ov_ref), so psql renders real "btree (...)" and "FOREIGN KEY ..."
// lines instead of blanks. psql looks for " USING " in an index def, so the
// index form includes it.
func rewriteObjectDefs(sql string) string {
	sql = rewriteFuncCallArg1(sql, "pg_get_indexdef", func(a string) string {
		return "(SELECT ' USING btree (' || ov_cols || ')' FROM pg_index WHERE indexrelid = " + a + ")"
	})
	sql = rewriteFuncCallArg1(sql, "pg_get_constraintdef", func(a string) string {
		return "(SELECT CASE contype" +
			" WHEN 'f' THEN 'FOREIGN KEY (' || ov_cols || ') REFERENCES ' || ov_ref" +
			" WHEN 'p' THEN 'PRIMARY KEY (' || ov_cols || ')'" +
			" WHEN 'u' THEN 'UNIQUE (' || ov_cols || ')'" +
			" ELSE '' END FROM pg_constraint WHERE oid = " + a + ")"
	})
	return sql
}

// rewriteFuncCallArg1 replaces each call name(arg1, ...) with build(arg1),
// matching balanced parentheses.
func rewriteFuncCallArg1(sql, name string, build func(arg1 string) string) string {
	low := strings.ToLower(sql)
	lname := strings.ToLower(name)
	for from := 0; ; {
		i := indexWordCall(low, lname, from)
		if i < 0 {
			return sql
		}
		open := i + len(name)
		for open < len(sql) && sql[open] == ' ' {
			open++
		}
		depth, j, arg1End := 0, open, -1
		for j < len(sql) {
			switch sql[j] {
			case '(':
				depth++
			case ')':
				depth--
			case ',':
				if depth == 1 && arg1End < 0 {
					arg1End = j
				}
			}
			if depth == 0 {
				break
			}
			j++
		}
		if depth != 0 {
			return sql
		}
		if arg1End < 0 {
			arg1End = j
		}
		repl := build(strings.TrimSpace(sql[open+1 : arg1End]))
		sql = sql[:i] + repl + sql[j+1:]
		low = strings.ToLower(sql)
		from = i + len(repl)
	}
}

func indexWordCall(low, name string, from int) int {
	for from < len(low) {
		i := strings.Index(low[from:], name)
		if i < 0 {
			return -1
		}
		i += from
		boundaryBefore := i == 0 || !isWordByte(low[i-1])
		k := i + len(name)
		for k < len(low) && low[k] == ' ' {
			k++
		}
		if boundaryBefore && k < len(low) && low[k] == '(' {
			return i
		}
		from = i + len(name)
	}
	return -1
}

func isWordByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// refPgCatalog strips the "pg_catalog." schema qualifier: our emulated catalog
// tables and functions live under bare names, and clients reference them both
// qualified and unqualified.
var rePgCatalog = regexp.MustCompile(`(?i)\bpg_catalog\.`)

func rewritePgCatalogPrefix(sql string) string {
	return rePgCatalog.ReplaceAllString(sql, "")
}

// rePublic matches a "public." (or "public".) schema qualifier. Clients think
// tables live in schema public; in SQLite they live in the (unqualified) main
// schema, so we drop the qualifier.
var rePublic = regexp.MustCompile(`(?i)"?\bpublic\b"?\.`)

func rewritePublicPrefix(sql string) string {
	return rePublic.ReplaceAllString(sql, "")
}

// reOperatorCall matches Postgres' explicit operator syntax, e.g.
// "OPERATOR(pg_catalog.~)", which psql emits for pattern matching.
var reOperatorCall = regexp.MustCompile(`(?i)OPERATOR\s*\(\s*([^)]+?)\s*\)`)

// rewriteOperatorSyntax unwraps OPERATOR(...) back to the bare operator so the
// rest of the dialect layer (and SQLite) can handle it.
func rewriteOperatorSyntax(sql string) string {
	return reOperatorCall.ReplaceAllStringFunc(sql, func(m string) string {
		inner := reOperatorCall.FindStringSubmatch(m)[1]
		if i := strings.LastIndex(inner, "."); i >= 0 { // drop any schema qualifier
			inner = inner[i+1:]
		}
		return " " + inner + " "
	})
}

// reCollate matches the default collations psql attaches to comparisons; SQLite
// has no matching collation, and they don't affect our results, so drop them.
var reCollate = regexp.MustCompile(`(?i)\s+COLLATE\s+"?(?:default|c|posix)"?`)

func rewriteCollate(sql string) string {
	return reCollate.ReplaceAllString(sql, "")
}

// rewriteMatchOperators maps Postgres POSIX-regex operators onto SQLite's
// REGEXP function: ~ (match), !~ (no match), and the *-suffixed
// case-insensitive variants.
func rewriteMatchOperators(sql string) string {
	for {
		op, idx := findMatchOp(sql)
		if idx < 0 {
			return sql
		}
		ls, le := operandBefore(sql, idx)
		rs, re := operandAfter(sql, idx+len(op))
		if ls == le || rs == re {
			return sql // malformed; avoid looping
		}
		left, right := sql[ls:le], sql[rs:re]
		if op[len(op)-1] == '*' { // case-insensitive
			right = injectCaseInsensitive(right)
		}
		repl := left + " REGEXP " + right
		if op[0] == '!' {
			repl = "NOT (" + left + " REGEXP " + right + ")"
		}
		sql = sql[:ls] + repl + sql[re:]
	}
}

// findMatchOp locates the first ~, !~, ~*, or !~* operator not inside a string
// literal, returning the operator text and the index of its first byte.
func findMatchOp(sql string) (string, int) {
	inStr := false
	for i := 0; i < len(sql); i++ {
		switch sql[i] {
		case '\'':
			inStr = !inStr
		case '~':
			if inStr {
				continue
			}
			start, op := i, "~"
			if i > 0 && sql[i-1] == '!' {
				start, op = i-1, "!~"
			}
			if i+1 < len(sql) && sql[i+1] == '*' {
				op += "*"
			}
			return op, start
		}
	}
	return "", -1
}

// operandAfter returns the [start,end) span of the operand starting at or after
// pos (skipping spaces): a string literal, a parenthesized group, or a token.
func operandAfter(sql string, pos int) (int, int) {
	for pos < len(sql) && sql[pos] == ' ' {
		pos++
	}
	if pos >= len(sql) {
		return pos, pos
	}
	switch sql[pos] {
	case '\'':
		j := pos + 1
		for j < len(sql) && sql[j] != '\'' {
			j++
		}
		return pos, min(j+1, len(sql))
	case '(':
		depth, j := 0, pos
		for j < len(sql) {
			if sql[j] == '(' {
				depth++
			} else if sql[j] == ')' {
				if depth--; depth == 0 {
					j++
					break
				}
			}
			j++
		}
		return pos, j
	default:
		j := pos
		for j < len(sql) && isOperandChar(sql[j]) {
			j++
		}
		return pos, j
	}
}

// injectCaseInsensitive prepends the (?i) flag inside a string-literal pattern.
func injectCaseInsensitive(operand string) string {
	if len(operand) >= 2 && operand[0] == '\'' && operand[len(operand)-1] == '\'' {
		return "'(?i)" + operand[1:]
	}
	return operand
}
