package postgres

import (
	"encoding/json"
	"sort"
	"strings"
)

// hstoreText renders a stored JSON object as Postgres hstore text
// (`"k"=>"v", …`), keys sorted for a stable form. A value that isn't a JSON
// object passes through.
func hstoreText(jsonStr string) string {
	var m map[string]any
	if json.Unmarshal([]byte(jsonStr), &m) != nil {
		return jsonStr
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		v := `NULL`
		if m[k] != nil {
			v = hstoreQuoteStr(hstoreScalar(m[k]))
		}
		parts[i] = hstoreQuoteStr(k) + "=>" + v
	}
	return strings.Join(parts, ", ")
}

func hstoreQuoteStr(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func hstoreScalar(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// hstore over SQLite: stored as a JSON object (see engine/hstore.go). A column
// declared `hstore` keeps that declared type (so we can format its output back
// to hstore text); the value flows through json1 for `->` and `?`.

// rewriteHstoreCast turns a `'k=>v'::hstore` literal into hstore_in('k=>v') (its
// JSON object), and drops the cast on a non-literal (already JSON).
func rewriteHstoreCast(sql string) string {
	low := strings.ToLower(sql)
	if !strings.Contains(low, "::hstore") && !strings.Contains(low, ":: hstore") {
		return sql
	}
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			j := endOfStringLiteral(sql, i)
			lit := sql[i:j]
			k := j
			for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t') {
				k++
			}
			if m := hstoreCastAt(sql, k); m > 0 {
				b.WriteString("hstore_in(" + lit + ")")
				i = m
				continue
			}
			b.WriteString(lit)
			i = j
			continue
		}
		if c == ':' && i+1 < len(sql) && sql[i+1] == ':' {
			if m := hstoreCastAt(sql, i); m > 0 { // expr::hstore -> drop cast
				i = m
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// hstoreCastAt reports the index past a `::hstore` starting at pos, or 0.
func hstoreCastAt(sql string, pos int) int {
	if pos+1 >= len(sql) || sql[pos] != ':' || sql[pos+1] != ':' {
		return 0
	}
	k := pos + 2
	for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t') {
		k++
	}
	if strings.HasPrefix(strings.ToLower(sql[k:]), "hstore") {
		e := k + len("hstore")
		if e >= len(sql) || !isIdentPart(sql[e]) {
			return e
		}
	}
	return 0
}
