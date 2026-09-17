package engine

import (
	"context"
	"strings"
)

// current_schema() and current_schemas() answer from the session search_path,
// so they have to be resolved per statement: the SQLite functions they would
// otherwise reach are registered globally and cannot see session state.
//
// They used to answer a constant "public" no matter what the path was, which
// misreports the schema a client is working in. gorm's Migrator, for one,
// filters information_schema by CURRENT_SCHEMA(), so it asked about the wrong
// schema and concluded every table was missing.

// resolveCurrentSchema replaces bare current_schema()/current_schemas() calls
// with the value this session's search_path gives them.
func resolveCurrentSchema(ctx context.Context, st *dbState, query string) string {
	lower := strings.ToLower(query)
	if !strings.Contains(lower, "current_schema") {
		return query
	}
	schema := currentSchema(ctx, st)

	out := replaceCallOutsideStrings(query, "current_schemas", func(args string) string {
		return sqlQuote(currentSchemas(ctx, st, strings.Contains(strings.ToLower(args), "true")))
	})
	return replaceCallOutsideStrings(out, "current_schema", func(string) string {
		if schema == "" {
			return "NULL"
		}
		return sqlQuote(schema)
	})
}

// currentSchema is the first schema in the search_path that exists, as
// Postgres defines it. With no path, or one naming nothing that exists, the
// answer is public.
func currentSchema(ctx context.Context, st *dbState) string {
	for _, s := range usableSearchPath(st, searchPathFrom(ctx)) {
		return s
	}
	return "public"
}

// currentSchemas renders the path as a Postgres array literal, optionally with
// the implicit pg_catalog in front.
func currentSchemas(ctx context.Context, st *dbState, includeImplicit bool) string {
	var names []string
	if includeImplicit {
		names = append(names, "pg_catalog")
	}
	names = append(names, usableSearchPath(st, searchPathFrom(ctx))...)
	names = append(names, "public")
	return "{" + strings.Join(names, ",") + "}"
}

// replaceCallOutsideStrings rewrites `name(...)` calls, passing the argument
// text to fn. A match inside a string or quoted identifier, or one that is part
// of a longer identifier or a qualified name, is left alone.
func replaceCallOutsideStrings(sql, name string, fn func(args string) string) string {
	var b strings.Builder
	lower := strings.ToLower(sql)
	i := 0
	for i < len(sql) {
		c := sql[i]
		if c == '\'' || c == '"' {
			j := skipLiteral(sql, i, c)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if !strings.HasPrefix(lower[i:], name) {
			b.WriteByte(c)
			i++
			continue
		}
		if i > 0 && (isIdentByte(sql[i-1]) || sql[i-1] == '.') {
			b.WriteByte(c)
			i++
			continue
		}
		j := i + len(name)
		for j < len(sql) && sql[j] == ' ' {
			j++
		}
		// current_schemas must not swallow a current_schema match and vice
		// versa: a longer identifier byte after the name means a different one.
		if j >= len(sql) || sql[j] != '(' {
			b.WriteByte(c)
			i++
			continue
		}
		end, args, ok := callArgs(sql, j)
		if !ok {
			b.WriteByte(c)
			i++
			continue
		}
		b.WriteString(fn(args))
		i = end
	}
	return b.String()
}

// callArgs returns the index just past the parenthesised argument list that
// starts at i, and the text between the parentheses.
func callArgs(sql string, i int) (end int, args string, ok bool) {
	depth := 0
	start := i + 1
	for i < len(sql) {
		switch sql[i] {
		case '\'', '"':
			i = skipLiteral(sql, i, sql[i])
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i + 1, sql[start:i], true
			}
		}
		i++
	}
	return 0, "", false
}
