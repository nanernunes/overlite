package postgres

import (
	"regexp"
	"strings"
)

// Exact numeric: a `numeric`/`decimal[(p,s)]` column is stored as its exact
// decimal string in a TEXT cell that carries the DECIMAL collation, so storage
// is lossless and comparison/ordering are numeric (see engine/decimal.go). We
// re-type the column to `DECIMALTEXT COLLATE DECIMAL`: DECIMALTEXT gives it TEXT
// affinity (SQLite stores the string verbatim) and lets overlite advertise OID
// 1700; the collation makes `<`/`>`/`ORDER BY` compare as numbers.

var reNumericType = regexp.MustCompile(`(?i)\b(?:numeric|decimal)\b(\s*\(\s*\d+\s*(?:,\s*\d+\s*)?\))?`)

// rewriteNumericColumns re-types numeric/decimal columns in a CREATE TABLE. It
// only touches CREATE TABLE so casts like `x::numeric` elsewhere are untouched.
// Any (precision, scale) is kept on the marker (DECIMALTEXT(10,2)) — SQLite
// ignores it for affinity, but information_schema.columns reports it.
func rewriteNumericColumns(sql string) string {
	if !strings.EqualFold(firstWordUpper(sql), "CREATE") || secondWordUpper(sql) != "TABLE" {
		return sql
	}
	return reNumericType.ReplaceAllString(sql, "DECIMALTEXT$1 COLLATE DECIMAL")
}
