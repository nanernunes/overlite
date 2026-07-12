package postgres

import (
	"regexp"
	"strings"
	"time"
)

// timestamptz over SQLite: a `timestamptz` column stores an absolute instant as
// bare UTC text (a valid, readable SQLite value). On input, a timestamp literal
// carrying an explicit offset is converted to UTC; on output we advertise the
// +00 offset and OID 1184 so clients read it as an instant. AT TIME ZONE
// converts via Go's zone database.

// isTimestamptzDecl reports whether a declared type is timestamp-with-time-zone.
func isTimestamptzDecl(decl string) bool {
	d := strings.ToUpper(decl)
	return strings.Contains(d, "TIMESTAMPTZ") ||
		(strings.Contains(d, "TIMESTAMP") && strings.Contains(d, "WITH TIME ZONE"))
}

// withUTCOffset tags a bare UTC timestamp string with the +00 offset (leaving a
// value that already has an offset alone).
func withUTCOffset(s string) string {
	s = strings.TrimRight(s, " ")
	i := strings.IndexByte(s, ':')
	if s == "" || i < 0 { // not a full timestamp
		return s
	}
	if strings.HasSuffix(s, "Z") || strings.ContainsAny(s[i:], "+-") {
		return s // already carries an offset
	}
	return s + "+00"
}

// reTimestamptzLit matches a timestamp string literal that carries an explicit
// zone offset (Z or ±HH[:MM]) — an unambiguous instant, normalized to UTC.
var reTimestamptzLit = regexp.MustCompile(
	`'(\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}(?::?\d{2})?))'`)

// rewriteTimestamptzLiteral converts offset-bearing timestamp literals to their
// bare UTC form, so storage and comparison are canonical.
func rewriteTimestamptzLiteral(sql string) string {
	if !strings.Contains(sql, "'") {
		return sql
	}
	return reTimestamptzLit.ReplaceAllStringFunc(sql, func(m string) string {
		if u, ok := toUTCLiteral(m[1 : len(m)-1]); ok {
			return "'" + u + "'"
		}
		return m
	})
}

// toUTCLiteral parses a timestamp with an explicit offset and returns the bare
// UTC representation.
func toUTCLiteral(s string) (string, bool) {
	s = strings.Replace(strings.TrimSpace(s), "T", " ", 1)
	ci := strings.IndexByte(s, ':')
	if ci < 0 {
		return "", false
	}
	var body, off string
	if strings.HasSuffix(s, "Z") {
		body, off = strings.TrimSuffix(s, "Z"), "+00:00"
	} else if k := strings.IndexAny(s[ci:], "+-"); k >= 0 {
		body, off = s[:ci+k], s[ci+k:]
	} else {
		return "", false
	}
	if off = normOffset(off); off == "" {
		return "", false
	}
	layout, outLayout := "2006-01-02 15:04:05Z07:00", "2006-01-02 15:04:05"
	if strings.Contains(body, ".") {
		layout, outLayout = "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999"
	}
	tm, err := time.Parse(layout, body+off)
	if err != nil {
		return "", false
	}
	return tm.UTC().Format(outLayout), true
}

// normOffset canonicalizes an offset (+02, -0500, +02:00, …) to ±HH:MM.
func normOffset(o string) string {
	if len(o) < 3 || (o[0] != '+' && o[0] != '-') {
		return ""
	}
	rest := strings.ReplaceAll(o[1:], ":", "")
	for len(rest) < 4 {
		rest += "0"
	}
	return string(o[0]) + rest[:2] + ":" + rest[2:4]
}

// rewriteAtTimeZone maps `expr AT TIME ZONE 'zone'` onto the engine function that
// converts an instant to a wall clock in that zone.
func rewriteAtTimeZone(sql string) string {
	const kw = "at time zone"
	for {
		low := strings.ToLower(sql)
		i := strings.Index(low, kw)
		if i < 0 || (i > 0 && isIdentPart(low[i-1])) {
			return sql
		}
		ls, le := operandBefore(sql, i)
		rs, re := operandAfter(sql, i+len(kw))
		if ls == le || rs == re {
			return sql
		}
		repl := "overlite_at_time_zone(" + sql[ls:le] + ", " + sql[rs:re] + ")"
		sql = sql[:ls] + repl + sql[re:]
	}
}
