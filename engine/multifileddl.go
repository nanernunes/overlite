package engine

import (
	"fmt"
	"regexp"
	"strings"
)

// In multi-file mode a schema is an attached SQLite database, and two pieces of
// DDL do not carry over from Postgres unchanged:
//
//   - CREATE INDEX qualifies the index, not the table. Postgres writes
//     `CREATE INDEX i ON sales.orders (...)`; SQLite wants
//     `CREATE INDEX sales.i ON orders (...)`, and rejects the other spelling.
//   - A REFERENCES clause cannot cross databases at all, so the qualifier has
//     to go when it names the table's own schema — and when it names a
//     different one, no rewrite can make it work.

// reQualifiedIndex captures a CREATE INDEX whose target table is qualified.
var reQualifiedIndex = regexp.MustCompile(
	`(?is)^(\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:IF\s+NOT\s+EXISTS\s+)?)` + // 1: head
		`("[^"]+"|\w+)` + // 2: index name
		`(\s+ON\s+)` + // 3: ON
		`(?:"([^"]+)"|(\w+))\s*\.\s*` + // 4/5: schema
		`("[^"]+"|\w+)`) // 6: table

// rewriteMultiFileIndex moves the schema from the table onto the index name.
func rewriteMultiFileIndex(query string) string {
	if !schemaFilesMode {
		return query
	}
	m := reQualifiedIndex.FindStringSubmatch(query)
	if m == nil {
		return query
	}
	schema := m[4]
	if schema == "" {
		schema = m[5]
	}
	if !isRegisteredSchema(schema) {
		return query
	}
	index := strings.Trim(m[2], `"`)
	return m[1] + quoteIdent(schema) + "." + `"` + index + `"` + m[3] + m[6] + query[len(m[0]):]
}

// reQualifiedReference captures a REFERENCES clause with a schema qualifier.
var reQualifiedReference = regexp.MustCompile(
	`(?is)\bREFERENCES\s+(?:"([^"]+)"|(\w+))\s*\.\s*("[^"]+"|\w+)`)

// rewriteMultiFileReferences drops the qualifier from a REFERENCES clause that
// names the statement's own schema, and reports one that names another.
func rewriteMultiFileReferences(query string) (string, error) {
	if !schemaFilesMode || !strings.Contains(strings.ToUpper(query), "REFERENCES") {
		return query, nil
	}
	owner := statementSchema(query)
	var failed string
	out := replaceAllSubmatchFunc(reQualifiedReference, query, func(m []string) string {
		schema := m[1]
		if schema == "" {
			schema = m[2]
		}
		if !isRegisteredSchema(schema) {
			return m[0]
		}
		if owner != "" && !strings.EqualFold(schema, owner) {
			failed = schema
			return m[0]
		}
		return "REFERENCES " + m[3]
	})
	if failed != "" {
		return "", fmt.Errorf(
			"a foreign key to schema %q cannot be enforced in multi-file schema mode: "+
				"each schema is a separate database file. Use the single-file mode "+
				"(unset OVERLITE_MULTITENANT_SCHEMA) for cross-schema references", failed)
	}
	return out, nil
}

// statementSchema returns the schema of the table a CREATE/ALTER TABLE targets.
func statementSchema(query string) string {
	m := regexp.MustCompile(`(?is)^\s*(?:CREATE|ALTER)\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"([^"]+)"|(\w+))\s*\.`).
		FindStringSubmatch(query)
	if m == nil {
		return ""
	}
	if m[1] != "" {
		return m[1]
	}
	return m[2]
}

// isRegisteredSchema reports whether name is one of this connection's schemas.
func isRegisteredSchema(name string) bool {
	for _, s := range cachedSchemas() {
		if strings.EqualFold(s, name) {
			return true
		}
	}
	return false
}

// replaceAllSubmatchFunc is ReplaceAllStringFunc with the submatches in hand.
func replaceAllSubmatchFunc(re *regexp.Regexp, s string, fn func([]string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(s, -1) {
		groups := make([]string, len(loc)/2)
		for i := range groups {
			if loc[2*i] >= 0 {
				groups[i] = s[loc[2*i]:loc[2*i+1]]
			}
		}
		b.WriteString(s[last:loc[0]])
		b.WriteString(fn(groups))
		last = loc[1]
	}
	b.WriteString(s[last:])
	return b.String()
}
