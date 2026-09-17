package postgres

import (
	"regexp"
	"strings"
)

// ILIKE is Postgres' case-insensitive LIKE. SQLite has no such operator, but
// its LIKE is already case-insensitive for ASCII, and the comparison it does is
// what Postgres' ILIKE does for the same input. Mapping the operator onto LIKE
// keeps the common case working instead of failing to parse; the two diverge
// only outside ASCII, where SQLite's LIKE is case-sensitive.

var (
	reILike    = regexp.MustCompile(`(?i)\bILIKE\b`)
	reNotILike = regexp.MustCompile(`(?i)\bNOT\s+ILIKE\b`)
)

// rewriteILike maps ILIKE onto LIKE, outside string literals.
func rewriteILike(sql string) string {
	if !strings.Contains(strings.ToUpper(sql), "ILIKE") {
		return sql
	}
	return mapOutsideStrings(sql, func(code string) string {
		code = reNotILike.ReplaceAllString(code, "NOT LIKE")
		return reILike.ReplaceAllString(code, "LIKE")
	})
}
