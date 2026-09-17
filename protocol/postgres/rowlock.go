package postgres

import (
	"regexp"
	"strings"
)

// Row-level locking clauses have no equivalent in SQLite, which serialises
// writers on the whole database instead. A SELECT ... FOR UPDATE therefore
// already holds the guarantee the clause is asking for, so dropping it keeps
// the statement meaning what it meant, rather than failing to parse.

// reRowLock matches the locking clause at the end of a SELECT, with its
// optional table list and wait behaviour.
var reRowLock = regexp.MustCompile(
	`(?is)\s+FOR\s+(?:UPDATE|NO\s+KEY\s+UPDATE|SHARE|KEY\s+SHARE)` +
		`(?:\s+OF\s+[^\s]+(?:\s*,\s*[^\s]+)*)?` +
		`(?:\s+NOWAIT|\s+SKIP\s+LOCKED)?`)

// rewriteRowLock drops a row-locking clause from a SELECT.
func rewriteRowLock(sql string) string {
	if !strings.Contains(strings.ToUpper(sql), " FOR ") {
		return sql
	}
	return mapOutsideStrings(sql, func(code string) string {
		return reRowLock.ReplaceAllString(code, "")
	})
}
