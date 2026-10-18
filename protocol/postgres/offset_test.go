package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteOffset(t *testing.T) {
	cases := map[string]string{
		// Prisma's reads: a bare OFFSET, at the top level and in a subquery.
		`SELECT "id" FROM "User" WHERE "role" IN ($1) OFFSET $2`:                      `SELECT "id" FROM "User" WHERE "role" IN ($1) LIMIT -1 OFFSET $2`,
		`SELECT COUNT(*) FROM (SELECT "id" FROM "User" WHERE 1=1 OFFSET $1) AS "sub"`: `SELECT COUNT(*) FROM (SELECT "id" FROM "User" WHERE 1=1 LIMIT -1 OFFSET $1) AS "sub"`,
		// Postgres takes either order; SQLite only LIMIT first.
		"SELECT a FROM t OFFSET 5 LIMIT 10":                  "SELECT a FROM t LIMIT 10 OFFSET 5",
		"SELECT a FROM (SELECT a FROM t OFFSET 1 LIMIT 2) s": "SELECT a FROM (SELECT a FROM t LIMIT 2 OFFSET 1) s",
		// The limit of an outer query does not cover a subquery, nor the reverse.
		"SELECT a FROM (SELECT a FROM t LIMIT 3) s OFFSET 1": "SELECT a FROM (SELECT a FROM t LIMIT 3) s LIMIT -1 OFFSET 1",
		"SELECT a FROM (SELECT a FROM t OFFSET 1) s LIMIT 3": "SELECT a FROM (SELECT a FROM t LIMIT -1 OFFSET 1) s LIMIT 3",
		// Already valid, or not the keyword at all: untouched.
		"SELECT a FROM t LIMIT 10 OFFSET 5":    "SELECT a FROM t LIMIT 10 OFFSET 5",
		"SELECT t.offset FROM t":               "SELECT t.offset FROM t",
		"SELECT 1 AS offset":                   "SELECT 1 AS offset",
		`SELECT "offset" FROM t`:               `SELECT "offset" FROM t`,
		"SELECT 'x OFFSET 1' FROM t":           "SELECT 'x OFFSET 1' FROM t",
		"SELECT offset_ms FROM t":              "SELECT offset_ms FROM t",
		"SELECT a FROM t ORDER BY a OFFSET 2;": "SELECT a FROM t ORDER BY a LIMIT -1 OFFSET 2;",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteOffset(in), "rewriteOffset(%q)", in)
	}
}
