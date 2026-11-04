package postgres

import "overlite/core"

/*
A boolean sent as text is stored as SQLite's 1 or 0. A client that binds
without describing first — node-postgres, which also never says a type —
sends a JS true as the text "true"; passed through as a string it was stored
as that text in a boolean column, and `WHERE flag` or `flag = true` never
matched it again. Describe already types such a parameter by its column
(paramTypes), and pgx then sends it in binary; this is the same inference,
at Bind, for the parameters that arrive as text.
*/

// coerceTextBools turns each text-format parameter whose type is boolean —
// declared by the client, or inferred from the column it goes into — into
// 1 or 0. A value that is not a Postgres boolean is left as it came.
func (s *session) coerceTextBools(prep *prepared, fmts []int, params []core.Value) {
	if prep.raw == "" || !anyTextBool(fmts, params) {
		return
	}
	if prep.bindOIDs == nil {
		prep.bindOIDs = s.paramTypes(prep.raw, prep.numParams, prep.paramOIDs)
	}
	for i, p := range params {
		str, ok := p.(string)
		if !ok || formatFor(fmts, i) != 0 || i >= len(prep.bindOIDs) || prep.bindOIDs[i] != oidBool {
			continue
		}
		if b, ok := core.ParseBool(str); ok {
			params[i] = boolValue(b)
		}
	}
}

// anyTextBool reports whether some text parameter reads as a boolean, so the
// column lookup runs only for statements that may need it.
func anyTextBool(fmts []int, params []core.Value) bool {
	for i, p := range params {
		if str, ok := p.(string); ok && formatFor(fmts, i) == 0 {
			if _, ok := core.ParseBool(str); ok {
				return true
			}
		}
	}
	return false
}

func boolValue(b bool) int64 {
	if b {
		return 1
	}
	return 0
}
