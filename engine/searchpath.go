package engine

import (
	"context"
	"strings"

	"overlite/core"
)

// search_path resolution. After explicit `x.t` qualifiers are handled (see
// schemaqualify.go), an unqualified table name in a table position is resolved
// against the session search_path: for a read it becomes the first path schema
// that actually has that table (an existence check, so a bare public name is
// never rewritten by mistake); for CREATE TABLE it goes to the first path
// schema, where Postgres creates it.

func searchPathFrom(ctx context.Context) []string {
	sp, _ := ctx.Value(core.SearchPathKey).([]string)
	return sp
}

// usableSearchPath returns the registered, non-public schemas named in the
// session path, in path order.
func usableSearchPath(st *dbState, sp []string) []string {
	if len(sp) == 0 {
		return nil
	}
	reg := map[string]bool{}
	for _, s := range st.schemaList() {
		reg[strings.ToLower(s)] = true
	}
	var out []string
	for _, s := range sp {
		ls := strings.ToLower(s)
		if ls == "public" || ls == "$user" || !reg[ls] {
			continue
		}
		out = append(out, s)
	}
	return out
}

// resolveSearchPath rewrites unqualified table references per the session
// search_path. It's a no-op with an empty or public-only path.
func resolveSearchPath(ctx context.Context, st *dbState, q querier, query string) string {
	path := usableSearchPath(st, searchPathFrom(ctx))
	if len(path) == 0 {
		return query
	}
	return rewriteUnqualifiedTables(ctx, q, query, path)
}

// tableIntro reports whether a keyword introduces a table name, and whether the
// position is a CREATE target (resolved to path[0], not by existence).
func tableIntro(prev, prev2 string) (intro, create bool) {
	switch prev {
	case "from", "join", "update", "truncate":
		return true, false
	case "into":
		return true, false // INSERT INTO
	case "table":
		// CREATE/ALTER/DROP/TRUNCATE TABLE
		switch prev2 {
		case "create":
			return true, true
		case "alter", "drop":
			return true, false
		}
	}
	return false, false
}

func rewriteUnqualifiedTables(ctx context.Context, q querier, sql string, path []string) string {
	var b strings.Builder
	prev, prev2 := "", ""
	i := 0
	for i < len(sql) {
		c := sql[i]
		if c == '\'' || c == '"' {
			j := skipLiteral(sql, i, c)
			b.WriteString(sql[i:j])
			i = j
			prev2, prev = prev, "\""
			continue
		}
		if isIdentByte(c) {
			start := i
			for i < len(sql) && (isIdentByte(sql[i]) || sql[i] == '.') {
				i++
			}
			word := sql[start:i]
			if intro, create := tableIntro(prev, prev2); intro && !strings.Contains(word, ".") {
				if repl, ok := resolveTableName(ctx, q, word, path, create); ok {
					b.WriteString(repl)
					prev2, prev = prev, strings.ToLower(word)
					continue
				}
			}
			b.WriteString(word)
			prev2, prev = prev, strings.ToLower(word)
			continue
		}
		b.WriteByte(c)
		i++
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			continue // whitespace keeps the keyword context
		}
		prev2, prev = "", "" // any other punctuation ends a table position
	}
	return b.String()
}

// resolveTableName maps a bare table name to its schema-qualified form. For a
// CREATE target it uses the first path schema; otherwise the first path schema
// that actually holds the table (so a public table is left untouched).
func resolveTableName(ctx context.Context, q querier, name string, path []string, create bool) (string, bool) {
	if create {
		return qualifyFor(path[0], name), true
	}
	for _, s := range path {
		if schemaHasTable(ctx, q, s, name) {
			return qualifyFor(s, name), true
		}
	}
	return "", false
}

// qualifyFor renders a schema-qualified name the way it is stored: one
// identifier holding both parts.
func qualifyFor(schema, name string) string {
	return `"` + schema + "." + name + `"`
}

// schemaHasTable reports whether schema holds a table or view called name.
func schemaHasTable(ctx context.Context, q querier, schema, name string) bool {
	rows, err := q.QueryContext(ctx,
		"SELECT 1 FROM main.sqlite_master WHERE type IN ('table','view') AND name = ? LIMIT 1",
		schema+"."+name)
	if err != nil {
		return false
	}
	defer rows.Close()
	return rows.Next()
}
