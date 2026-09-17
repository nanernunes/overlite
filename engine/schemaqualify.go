package engine

import (
	"regexp"
	"strings"
	"sync"
)

// In single-file mode a schema-qualified name like `vendas.pedidos` must reach
// SQLite as the single quoted identifier `"vendas.pedidos"` (the real table
// name), not as attached-db.table. This is the one place that distinguishes a
// `schema.table` qualifier from an `alias.column` one: only a qualifier whose
// first part is a *registered schema* is rewritten.
//
// The registered-schema set is cached (refreshed by setupConnection and after
// CREATE/DROP SCHEMA) so the rewrite doesn't hit the database per statement.

var (
	schemaCacheMu    sync.RWMutex
	schemaCacheNames []string
)

func setSchemaCache(names []string) {
	schemaCacheMu.Lock()
	schemaCacheNames = append(schemaCacheNames[:0:0], names...)
	schemaCacheMu.Unlock()
}

func cachedSchemas() []string {
	schemaCacheMu.RLock()
	defer schemaCacheMu.RUnlock()
	return schemaCacheNames
}

// qualifySchemaNames rewrites `<schema>.<name>` → `"<schema>.<name>"` for every
// registered schema, outside string/identifier literals. A no-op when no schema
// is registered.
func qualifySchemaNames(query string) string {
	if !strings.Contains(query, ".") {
		return query
	}
	schemas := cachedSchemas()
	for _, s := range schemas {
		query = qualifyOneSchema(query, s)
	}
	return prefixSchemaIndexName(query)
}

// reCreateIndex captures a CREATE INDEX's index name and its (already-qualified)
// target table.
var reCreateIndex = regexp.MustCompile(
	`(?is)^(\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?)("[^"]+"|[A-Za-z_]\w*)(\s+ON\s+)("[^"]+")`)

// prefixSchemaIndexName gives an index on a schema table the same "<schema>."
// prefix as the table, so it belongs to that schema (and pg_index/\d find it).
// The target has already been rewritten to "<schema>.<table>" by this point.
func prefixSchemaIndexName(query string) string {
	m := reCreateIndex.FindStringSubmatch(query)
	if m == nil {
		return query
	}
	schema, _ := splitStoredName(strings.Trim(m[4], `"`))
	if schema == "" {
		return query // target isn't schema-qualified: index stays in public
	}
	idx := strings.Trim(m[2], `"`)
	newName := `"` + schema + "." + idx + `"`
	return m[1] + newName + m[3] + m[4] + query[len(m[0]):]
}

// splitStoredName splits a stored table name "schema.table" into its parts; a
// name without a dot has an empty schema.
func splitStoredName(name string) (schema, table string) {
	if i := strings.IndexByte(name, '.'); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

func qualifyOneSchema(sql, schema string) string {
	var b strings.Builder
	prev, prev2 := "", ""
	i := 0
	for i < len(sql) {
		if sql[i] == '\'' {
			j := skipLiteral(sql, i, '\'')
			b.WriteString(sql[i:j])
			i = j
			prev2, prev = prev, "'"
			continue
		}
		// A qualifier starts at a word boundary. Anything glued to an
		// identifier byte or a dot belongs to a longer name.
		if i == 0 || (!isIdentByte(sql[i-1]) && sql[i-1] != '.') {
			if table, end, ok := matchQualifier(sql, i, schema); ok {
				b.WriteString(quoteIdent(schema + "." + table))
				// The statement may go on to qualify columns with the bare
				// table name, which every ORM does: `FROM sales.orders WHERE
				// orders.id = $1`. Renaming the table would strand those, so
				// the old name is kept as an alias.
				if needsAlias(sql, end, prev, prev2) {
					b.WriteString(` AS ` + quoteIdent(table))
				}
				i = end
				prev2, prev = prev, table
				continue
			}
		}
		if sql[i] == '"' {
			j := skipLiteral(sql, i, '"')
			b.WriteString(sql[i:j])
			word := strings.Trim(sql[i:j], `"`)
			i = j
			prev2, prev = prev, strings.ToLower(word)
			continue
		}
		if isIdentByte(sql[i]) {
			start := i
			for i < len(sql) && isIdentByte(sql[i]) {
				i++
			}
			b.WriteString(sql[start:i])
			prev2, prev = prev, strings.ToLower(sql[start:i])
			continue
		}
		c := sql[i]
		b.WriteByte(c)
		i++
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue // whitespace keeps the keyword context
		}
		prev2, prev = "", ""
	}
	return b.String()
}

// needsAlias reports whether a rewritten table reference should carry its
// original name as an alias: it must sit where a table may be aliased, and not
// already have one.
func needsAlias(sql string, end int, prev, prev2 string) bool {
	switch prev {
	case "from", "join", "update":
	case "into":
		if prev2 == "insert" {
			break
		}
		return false
	default:
		return false // CREATE/ALTER/DROP TABLE take no alias
	}

	j := end
	for j < len(sql) && (sql[j] == ' ' || sql[j] == '\t' || sql[j] == '\n' || sql[j] == '\r') {
		j++
	}
	if j >= len(sql) {
		return true
	}
	if sql[j] == '"' { // an explicitly quoted alias
		return false
	}
	start := j
	for j < len(sql) && isIdentByte(sql[j]) {
		j++
	}
	if j == start {
		return true // punctuation: "(", ",", ";" — no alias present
	}
	// A word follows: a clause keyword means no alias, anything else is one.
	return clauseKeywords[strings.ToLower(sql[start:j])]
}

// clauseKeywords are the words that may follow a table reference without being
// an alias for it.
var clauseKeywords = map[string]bool{
	"where": true, "set": true, "values": true, "select": true, "join": true,
	"inner": true, "left": true, "right": true, "full": true, "cross": true,
	"natural": true, "on": true, "using": true, "group": true, "order": true,
	"limit": true, "offset": true, "having": true, "union": true, "returning": true,
	"default": true, "as": false,
}

// matchQualifier reads a `<schema>.<name>` qualifier at i, in any of the four
// quoting combinations a client may send — `sales.orders`, `"sales".orders`,
// `sales."orders"`, `"sales"."orders"`. Most ORMs quote both halves, so
// matching only the bare spelling left them unable to use schemas at all.
//
// It returns the unquoted table name and the index just past the qualifier.
func matchQualifier(sql string, i int, schema string) (table string, end int, ok bool) {
	name, j, ok := readIdent(sql, i)
	if !ok || !strings.EqualFold(name, schema) {
		return "", 0, false
	}
	if j >= len(sql) || sql[j] != '.' {
		return "", 0, false
	}
	table, k, ok := readIdent(sql, j+1)
	if !ok {
		return "", 0, false // `sales.*` and the like are not a table qualifier
	}
	return table, k, true
}

// readIdent reads one identifier at i, quoted or bare, and returns its value
// with any quoting removed.
func readIdent(sql string, i int) (name string, end int, ok bool) {
	if i >= len(sql) {
		return "", 0, false
	}
	if sql[i] == '"' {
		j := skipLiteral(sql, i, '"')
		if j <= i+1 || sql[j-1] != '"' {
			return "", 0, false // unterminated
		}
		return strings.ReplaceAll(sql[i+1:j-1], `""`, `"`), j, true
	}
	start := i
	for i < len(sql) && isIdentByte(sql[i]) {
		i++
	}
	if i == start {
		return "", 0, false
	}
	return sql[start:i], i, true
}

// skipLiteral returns the index just past a quoted run starting at i (quote q),
// honoring doubled-quote escapes: two single quotes, or two double quotes.
func skipLiteral(s string, i int, q byte) int {
	i++ // opening quote
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

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}
