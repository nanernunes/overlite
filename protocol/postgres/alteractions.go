package postgres

import (
	"regexp"
	"strings"
)

// Postgres takes several actions in one ALTER TABLE, comma-separated:
//
//	ALTER TABLE "School" ADD COLUMN "nickname" TEXT,
//	ADD COLUMN "rank" INTEGER NOT NULL DEFAULT 0;
//
// which is how Prisma writes a migration that changes more than one column of
// a table. SQLite's ALTER TABLE does one thing at a time and stops at the
// comma with a syntax error. Each action becomes an ALTER TABLE of its own,
// run in order inside the batch's transaction, so they still apply together
// or not at all.

// reAlterTableHead matches `ALTER TABLE [IF EXISTS] [ONLY] <table>` and what
// follows it, the table possibly schema-qualified and quoted.
var reAlterTableHead = regexp.MustCompile(`(?is)^(\s*ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` +
	`(?:"(?:[^"]|"")+"|[\w$]+)(?:\s*\.\s*(?:"(?:[^"]|"")+"|[\w$]+))?)\s+(.*?)\s*;?\s*$`)

// splitAlterActions returns one ALTER TABLE per action of stmt, or stmt alone
// when it is not an ALTER TABLE with more than one action.
func splitAlterActions(stmt string) []string {
	body := stripLeadingComments(stmt)
	if !strings.EqualFold(firstWordUpper(body), "ALTER") || secondWordUpper(body) != "TABLE" {
		return []string{stmt}
	}
	m := reAlterTableHead.FindStringSubmatch(body)
	if m == nil {
		return []string{stmt}
	}
	actions := splitTopLevel(m[2])
	if len(actions) < 2 {
		return []string{stmt}
	}
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, strings.TrimSpace(m[1])+" "+strings.TrimSpace(a))
	}
	return out
}
