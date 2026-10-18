package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitAlterActions(t *testing.T) {
	cases := map[string][]string{
		// Prisma's two-column change.
		"-- AlterTable\nALTER TABLE \"School\" ADD COLUMN     \"nickname\" TEXT,\nADD COLUMN     \"rank\" INTEGER NOT NULL DEFAULT 0": {
			`ALTER TABLE "School" ADD COLUMN     "nickname" TEXT`,
			`ALTER TABLE "School" ADD COLUMN     "rank" INTEGER NOT NULL DEFAULT 0`,
		},
		`ALTER TABLE IF EXISTS ONLY public.t DROP COLUMN a, ADD COLUMN b numeric(10, 2) DEFAULT 'x,y';`: {
			`ALTER TABLE IF EXISTS ONLY public.t DROP COLUMN a`,
			`ALTER TABLE IF EXISTS ONLY public.t ADD COLUMN b numeric(10, 2) DEFAULT 'x,y'`,
		},
		// One action, or not an ALTER TABLE: as is.
		`ALTER TABLE t ADD CONSTRAINT k FOREIGN KEY (a, b) REFERENCES u (a, b)`: {`ALTER TABLE t ADD CONSTRAINT k FOREIGN KEY (a, b) REFERENCES u (a, b)`},
		`SELECT a, b FROM t`: {`SELECT a, b FROM t`},
	}
	for in, want := range cases {
		assert.Equalf(t, want, splitAlterActions(in), "splitAlterActions(%q)", in)
	}
}
