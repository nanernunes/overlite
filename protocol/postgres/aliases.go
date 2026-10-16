package postgres

import (
	"regexp"
	"strings"
)

/*
Postgres takes any keyword as a column label after AS — GORM's index listing
reads "i.indisprimary AS primary" — while SQLite refuses many of its own
keywords there ("near primary: syntax error"). Those labels are quoted, which
both read the same.

Not every keyword: SELECT, VALUES and NOT follow AS in statements of their own
(CREATE VIEW v AS SELECT, CREATE TABLE t AS VALUES, a CTE AS NOT MATERIALIZED),
and a label is never followed by "(" — that is a CTE's body or a generated
column's expression.
*/
var reKeywordLabel = regexp.MustCompile(`(?i)\bAS\s+(primary|index|unique|default|check|references|table|order|group|constraint|foreign|collate|transaction|set|join|using|limit|to|add|isnull|notnull|nothing|returning|escape|autoincrement|deferrable|between|case|else|exists|all|distinct|union|except|intersect|into|alter|drop|create|commit|from|where|when|then)\b(\s*\()?`)

func rewriteKeywordLabels(sql string) string {
	return reKeywordLabel.ReplaceAllStringFunc(sql, func(m string) string {
		sub := reKeywordLabel.FindStringSubmatch(m)
		if sub[2] != "" {
			return m
		}
		return m[:len(m)-len(sub[1])] + `"` + strings.ToLower(sub[1]) + `"`
	})
}

/*
DROP … CASCADE / RESTRICT: GORM drops a table with CASCADE, pg_dump scripts
with either. SQLite has neither — dropping a table leaves views on it to fail
when used, much as RESTRICT would have refused — so the clause goes.
*/
var reDropTail = regexp.MustCompile(`(?is)^(\s*DROP\s+(?:TABLE|VIEW|INDEX|TRIGGER)\b.*?)\s+(?:CASCADE|RESTRICT)\s*;?\s*$`)

func rewriteDropCascade(sql string) string {
	return reDropTail.ReplaceAllString(sql, "$1")
}
