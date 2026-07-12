package engine

import (
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
// registered schema, outside string/identifier literals. A no-op in multi-file
// mode or when no schema is registered.
func qualifySchemaNames(query string) string {
	if schemaFilesMode || !strings.Contains(query, ".") {
		return query
	}
	schemas := cachedSchemas()
	for _, s := range schemas {
		query = qualifyOneSchema(query, s)
	}
	return query
}

func qualifyOneSchema(sql, schema string) string {
	low := strings.ToLower(sql)
	ls := strings.ToLower(schema)
	var b strings.Builder
	i := 0
	for i < len(sql) {
		switch sql[i] {
		case '\'':
			j := skipLiteral(sql, i, '\'')
			b.WriteString(sql[i:j])
			i = j
			continue
		case '"':
			j := skipLiteral(sql, i, '"')
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		// A registered schema at a word boundary, immediately followed by ".".
		if (i == 0 || !isIdentByte(sql[i-1])) && strings.HasPrefix(low[i:], ls) {
			k := i + len(schema)
			if k < len(sql) && sql[k] == '.' && (k == len(sql)-1 || sql[k+1] != '.') {
				t := k + 1
				start := t
				for t < len(sql) && isIdentByte(sql[t]) {
					t++
				}
				if t > start {
					b.WriteString(`"` + sql[i:k] + "." + sql[start:t] + `"`)
					i = t
					continue
				}
			}
		}
		b.WriteByte(sql[i])
		i++
	}
	return b.String()
}

// skipLiteral returns the index just past a quoted run starting at i (quote q),
// honoring doubled-quote escapes ('' or "").
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
