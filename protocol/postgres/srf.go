package postgres

import (
	"strconv"
	"strings"
)

// Set-returning functions in a select list: Postgres runs `SELECT unnest(a),
// unnest(b), generate_subscripts(a, 1) FROM t` as one row per array element,
// the calls stepping through their arrays together. SQLite can't return a set
// from the select list, so the set moves into FROM as a json_each over the
// first array, and every call in the list reads the same element position.
// Catalog introspection is written this way: Prisma pairs pg_constraint's
// conkey with confkey, and pg_index's indkey with indclass and indoption.
//
// Postgres pads the shorter arrays with NULLs to the longest; here the first
// array sets the row count, which is the same thing whenever the arrays have
// one length (as the catalog's paired columns always do). The arrays may be
// stored JSON arrays, '{…}' text or the catalog's "1 2 3" vectors; see
// overlite_array_json.

// srfNames are the set-returning calls moved out of a select list.
var srfNames = []string{"unnest", "generate_subscripts"}

// srfClauseWords end a FROM clause.
var srfClauseWords = []string{"where", "group", "having", "window", "order", "limit", "offset", "union", "except", "intersect"}

// rewriteSelectListSRFs rewrites every select list, at any nesting level (a
// subquery, a CTE body), that calls unnest or generate_subscripts.
func rewriteSelectListSRFs(sql string) string {
	low := strings.ToLower(sql)
	if !strings.Contains(low, "unnest") && !strings.Contains(low, "generate_subscripts") {
		return sql
	}
	n := 0
	for from := 0; ; {
		i := nextSRFCall(strings.ToLower(sql), from)
		if i < 0 {
			return sql
		}
		start, end := enclosingParens(sql, i)
		sub, ok := rewriteScopeSRFs(sql[start:end], i-start, "_srf"+strconv.Itoa(n))
		if !ok {
			from = i + 1
			continue
		}
		n++
		sql = sql[:start] + sub + sql[end:]
		from = start
	}
}

// nextSRFCall finds the next unnest(…) or generate_subscripts(…) call.
func nextSRFCall(low string, from int) int {
	best := -1
	for _, name := range srfNames {
		if i := indexWordCall(low, name, from); i >= 0 && (best < 0 || i < best) {
			best = i
		}
	}
	return best
}

// enclosingParens returns the bounds of the parenthesized text around pos (just
// inside the parens), or the whole statement when pos is at the top level.
func enclosingParens(sql string, pos int) (int, int) {
	var opens []int
	for i := 0; i < pos; i++ {
		switch sql[i] {
		case '\'':
			i = endOfStringLiteral(sql, i) - 1
		case '(':
			opens = append(opens, i)
		case ')':
			if len(opens) > 0 {
				opens = opens[:len(opens)-1]
			}
		}
	}
	if len(opens) == 0 {
		return 0, len(sql)
	}
	start := opens[len(opens)-1]
	if _, end, ok := readParenArgs(sql, start); ok {
		return start + 1, end - 1
	}
	return 0, len(sql)
}

// srfCall is one set-returning call in a select list.
type srfCall struct {
	start, end int    // the call's span in the scope
	name       string // unnest or generate_subscripts
	array      string // its array argument
}

// rewriteScopeSRFs rewrites the query in scope (one nesting level) whose
// select list holds the call at pos. ok is false when the call isn't in a
// select list followed by a FROM (a FROM-clause unnest is rewriteUnnest's).
func rewriteScopeSRFs(scope string, pos int, alias string) (string, bool) {
	if parenDepthAt(scope, pos) != 0 {
		return scope, false
	}
	sel := topLevelKeyword(scope, "select")
	if sel < 0 || sel > pos {
		return scope, false
	}
	f := topLevelKeyword(scope[pos:], "from")
	if f < 0 {
		return scope, false
	}
	fromIdx := pos + f

	// Every call in the select list, outermost level only.
	var calls []srfCall
	low := strings.ToLower(scope)
	for at := sel; ; {
		i := nextSRFCall(low[:fromIdx], at)
		if i < 0 {
			break
		}
		name := "unnest"
		if strings.HasPrefix(low[i:], "generate_subscripts") {
			name = "generate_subscripts"
		}
		open := strings.IndexByte(scope[i:], '(') + i
		args, end, ok := readParenArgs(scope, open)
		if !ok {
			return scope, false
		}
		at = end
		if parenDepthAt(scope, i) != 0 {
			continue
		}
		calls = append(calls, srfCall{i, end, name, strings.TrimSpace(splitTopLevel(args)[0])})
	}
	if len(calls) == 0 {
		return scope, false
	}

	// The FROM clause ends at the next clause keyword of this level, or before
	// the statement's closing semicolon.
	fromEnd := len(strings.TrimRight(scope, "; \t\r\n"))
	for _, kw := range srfClauseWords {
		if p := topLevelKeyword(scope[fromIdx+len("from"):], kw); p >= 0 && fromIdx+len("from")+p < fromEnd {
			fromEnd = fromIdx + len("from") + p
		}
	}

	driver := calls[0].array
	var b strings.Builder
	last := 0
	for _, c := range calls {
		b.WriteString(scope[last:c.start])
		switch {
		case c.name == "generate_subscripts":
			b.WriteString("(" + alias + ".key + overlite_array_lower(" + c.array + "))")
		case c.array == driver:
			b.WriteString(alias + ".value")
		default:
			b.WriteString("json_extract(overlite_array_json(" + c.array + "), '$[' || " + alias + ".key || ']')")
		}
		last = c.end
	}
	// The join goes after any line comment closing the FROM clause, not into it.
	fromText := strings.TrimRight(scope[last:fromEnd], " \t\r\n")
	if nl := strings.LastIndexByte(fromText, '\n'); strings.Contains(fromText[nl+1:], "--") {
		fromText += "\n"
	}
	b.WriteString(fromText)
	b.WriteString(", json_each(overlite_array_json(" + driver + ")) " + alias)
	if rest := scope[fromEnd:]; rest != "" && !strings.HasPrefix(strings.TrimLeft(rest, " \t\r\n"), ";") {
		b.WriteString(" ")
	}
	b.WriteString(scope[fromEnd:])
	return b.String(), true
}
