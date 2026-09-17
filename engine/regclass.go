package engine

import (
	"strings"
)

// to_regclass(name) resolves a relation name to its oid, or NULL when there is
// no such relation. It is the cheap "does this table exist?" check, and clients
// only ever compare the result against NULL.
//
// It is resolved per statement rather than registered as a SQLite function
// because the answer depends on the connection's schemas, which a globally
// registered function cannot see.

// resolveToRegclass replaces to_regclass('<name>') with a subquery over the
// catalog, so the result is an oid or NULL exactly as Postgres gives it.
func resolveToRegclass(query string) string {
	if !strings.Contains(strings.ToLower(query), "to_regclass") {
		return query
	}
	return replaceCallOutsideStrings(query, "to_regclass", func(args string) string {
		expr := strings.TrimSpace(args)
		if name, ok := stringLiteral(expr); ok {
			schema, table := splitRelationName(name)
			where := "c.relname = " + sqlQuote(table)
			if schema != "" {
				where += " AND n.nspname = " + sqlQuote(schema)
			}
			return "(SELECT c.oid FROM pg_class c" +
				" JOIN pg_namespace n ON n.oid = c.relnamespace" +
				" WHERE " + where + " LIMIT 1)"
		}
		// A bound parameter or any other expression is split in SQL instead.
		// It is named once, in a derived table, so a placeholder is not
		// duplicated into a second bind position.
		return "(SELECT c.oid FROM (SELECT " + expr + " AS v) AS _r" +
			" JOIN pg_namespace n JOIN pg_class c ON c.relnamespace = n.oid" +
			" WHERE CASE WHEN instr(_r.v, '.') > 0" +
			"  THEN n.nspname = substr(_r.v, 1, instr(_r.v, '.') - 1)" +
			"   AND c.relname = substr(_r.v, instr(_r.v, '.') + 1)" +
			"  ELSE c.relname = _r.v END LIMIT 1)"
	})
}

// stringLiteral unwraps a single-quoted SQL literal.
func stringLiteral(expr string) (string, bool) {
	if len(expr) < 2 || expr[0] != '\'' || expr[len(expr)-1] != '\'' {
		return "", false
	}
	return strings.ReplaceAll(expr[1:len(expr)-1], "''", "'"), true
}

// splitRelationName splits an optionally schema-qualified relation name,
// dropping the quoting Postgres allows on either part.
func splitRelationName(name string) (schema, table string) {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return unquoteRelationPart(name[:i]), unquoteRelationPart(name[i+1:])
	}
	return "", unquoteRelationPart(name)
}

func unquoteRelationPart(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}
