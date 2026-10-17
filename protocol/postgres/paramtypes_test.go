package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParamColumns(t *testing.T) {
	tbl, cols := paramColumns(`INSERT INTO "public"."c" ("id","on",n) VALUES ($1,$2,coalesce($3, 'a,b')),($4,$5,$6) ON CONFLICT ("id") DO UPDATE SET "on"="excluded"."on" RETURNING "id"`)
	assert.Equal(t, `"public"."c"`, tbl)
	assert.Equal(t, map[int]string{1: "id", 2: "on", 4: "id", 5: "on", 6: "n"}, cols)

	tbl, cols = paramColumns(`UPDATE "c" SET "on"=$1,"n"=$2 WHERE "c"."id" = $3 AND x IN ($4)`)
	assert.Equal(t, `"c"`, tbl)
	assert.Equal(t, map[int]string{1: "on", 2: "n", 3: "id"}, cols)

	tbl, cols = paramColumns(`SELECT * FROM "c" WHERE on = $1 LIMIT 1`)
	assert.Equal(t, `"c"`, tbl)
	assert.Equal(t, map[int]string{1: "on"}, cols)

	tbl, _ = paramColumns(`SELECT * FROM a JOIN b ON a.id = b.id WHERE b.on = $1`)
	assert.Empty(t, tbl, "a join could mean either table's column")
}
