package engine

import (
	"regexp"
	"strings"
)

// rePublicQualifier matches an explicit `public.` qualifier, quoted or not.
// Clients think their tables live in schema public; in SQLite they live in the
// unqualified main schema, so the qualifier is dropped.
var rePublicQualifier = regexp.MustCompile(`(?i)"?\bpublic\b"?\.`)

// stripPublicQualifier removes an explicit public schema qualifier, outside
// string and identifier literals.
//
// It runs after search_path resolution, not before: `public.t` names the
// public table even when the path points elsewhere, and stripping the
// qualifier first made it an unqualified name that resolution then moved into
// the path's schema.
func stripPublicQualifier(query string) string {
	if !strings.Contains(query, ".") {
		return query
	}
	var b strings.Builder
	i := 0
	for i < len(query) {
		c := query[i]
		if c == '\'' {
			j := skipLiteral(query, i, '\'')
			b.WriteString(query[i:j])
			i = j
			continue
		}
		// A quoted identifier is only skipped when it is not the public
		// qualifier itself: `"public"."t"` must still lose its first half.
		if c == '"' {
			if loc := rePublicQualifier.FindStringIndex(query[i:]); loc != nil && loc[0] == 0 {
				i += loc[1]
				continue
			}
			j := skipLiteral(query, i, '"')
			b.WriteString(query[i:j])
			i = j
			continue
		}
		if (i == 0 || (!isIdentByte(query[i-1]) && query[i-1] != '.')) &&
			(c == 'p' || c == 'P') {
			if loc := rePublicQualifier.FindStringIndex(query[i:]); loc != nil && loc[0] == 0 {
				i += loc[1]
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}
