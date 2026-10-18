package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteCatalogBoolLiterals(t *testing.T) {
	cases := map[string]string{
		// Prisma's table listing.
		"SELECT 1 FROM pg_class tbl WHERE tbl.relkind = 'r' AND tbl.relispartition = 'f'": "SELECT 1 FROM pg_class tbl WHERE tbl.relkind = 'r' AND tbl.relispartition = 0",
		"SELECT 1 FROM pg_index WHERE indisunique = 'true' AND indisprimary <> 'f'":       "SELECT 1 FROM pg_index WHERE indisunique = 1 AND indisprimary <> 0",
		"SELECT 1 FROM pg_attribute WHERE attnotnull='t'":                                 "SELECT 1 FROM pg_attribute WHERE attnotnull=1",
		// Not a catalog boolean, or not a boolean literal: untouched.
		"SELECT 1 FROM users WHERE active = 'f'":          "SELECT 1 FROM users WHERE active = 'f'",
		"SELECT 1 FROM pg_class WHERE relkind = 'r'":      "SELECT 1 FROM pg_class WHERE relkind = 'r'",
		"SELECT 1 FROM pg_class WHERE relispartition = 0": "SELECT 1 FROM pg_class WHERE relispartition = 0",
		"SELECT 1 FROM t WHERE relispartition_note = 'f'": "SELECT 1 FROM t WHERE relispartition_note = 'f'",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteCatalogBoolLiterals(in), "rewriteCatalogBoolLiterals(%q)", in)
	}
}
