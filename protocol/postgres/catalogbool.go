package postgres

import (
	"regexp"
	"sort"
	"strings"
)

// The catalogs keep their boolean columns as SQLite integers (0/1), which is
// what a bare `WHERE indisunique` or `NOT relispartition` wants. Postgres also
// lets a boolean meet a string literal ('t', 'f', 'true', ...), and the literal
// takes the column's type; SQLite compares 0 with 'f' as two different values
// and finds nothing. Introspection writes it that way: Prisma lists tables with
// `relkind = 'r' AND relispartition = 'f'`, which matched no table at all.
//
// Only the catalog's boolean columns (boolCatalogColumns) are rewritten, by
// name: a user table's text column compared with 't' stays a text comparison.

// reCatalogBoolLiteral matches `[qualifier.]<catalog bool column> <op> '<literal>'`
// for the spellings Postgres accepts as a boolean literal.
var reCatalogBoolLiteral = func() *regexp.Regexp {
	names := make([]string, 0, len(boolCatalogColumns))
	for name := range boolCatalogColumns {
		names = append(names, regexp.QuoteMeta(name))
	}
	sort.Strings(names)
	return regexp.MustCompile(`(?i)\b((?:\w+\.)?(?:` + strings.Join(names, "|") +
		`))(\s*(?:=|<>|!=)\s*)'(t|true|y|yes|on|1|f|false|n|no|off|0)'`)
}()

// rewriteCatalogBoolLiterals turns the literal compared with a catalog boolean
// column into the 1/0 the column holds.
func rewriteCatalogBoolLiterals(sql string) string {
	if !strings.Contains(sql, "'") {
		return sql
	}
	return reCatalogBoolLiteral.ReplaceAllStringFunc(sql, func(m string) string {
		sub := reCatalogBoolLiteral.FindStringSubmatch(m)
		v := "0"
		switch strings.ToLower(sub[3]) {
		case "t", "true", "y", "yes", "on", "1":
			v = "1"
		}
		return sub[1] + sub[2] + v
	})
}
