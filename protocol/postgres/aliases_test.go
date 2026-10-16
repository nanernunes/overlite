package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteKeywordLabels(t *testing.T) {
	cases := map[string]string{
		"SELECT i.indisprimary AS primary, i.indisunique AS non_unique FROM pg_index i": `SELECT i.indisprimary AS "primary", i.indisunique AS non_unique FROM pg_index i`,
		"SELECT x AS Order FROM t":                       `SELECT x AS "order" FROM t`,
		"CREATE VIEW v AS SELECT 1":                      "CREATE VIEW v AS SELECT 1",
		"WITH c AS (SELECT 1) SELECT * FROM c":           "WITH c AS (SELECT 1) SELECT * FROM c",
		"SELECT CAST(x AS int) FROM t":                   "SELECT CAST(x AS int) FROM t",
		"CREATE TABLE t (a int GENERATED ALWAYS AS (1))": "CREATE TABLE t (a int GENERATED ALWAYS AS (1))",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteKeywordLabels(in), "rewriteKeywordLabels(%q)", in)
	}
}

func TestRewriteDropCascade(t *testing.T) {
	cases := map[string]string{
		`DROP TABLE IF EXISTS "things" CASCADE`: `DROP TABLE IF EXISTS "things"`,
		"DROP VIEW v RESTRICT;":                 "DROP VIEW v",
		"DROP TABLE t":                          "DROP TABLE t",
		"DROP SCHEMA s CASCADE":                 "DROP SCHEMA s CASCADE", // the schema manager models it
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteDropCascade(in), "rewriteDropCascade(%q)", in)
	}
}
