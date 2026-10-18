package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParamColumns(t *testing.T) {
	tbl, cols, _ := paramColumns(`INSERT INTO "public"."c" ("id","on",n) VALUES ($1,$2,coalesce($3, 'a,b')),($4,$5,$6) ON CONFLICT ("id") DO UPDATE SET "on"="excluded"."on" RETURNING "id"`)
	assert.Equal(t, `"public"."c"`, tbl)
	assert.Equal(t, map[int]string{1: "id", 2: "on", 4: "id", 5: "on", 6: "n"}, cols)

	tbl, cols, _ = paramColumns(`UPDATE "c" SET "on"=$1,"n"=$2 WHERE "c"."id" = $3 AND x IN ($4)`)
	assert.Equal(t, `"c"`, tbl)
	assert.Equal(t, map[int]string{1: "on", 2: "n", 3: "id"}, cols)

	tbl, cols, _ = paramColumns(`SELECT * FROM "c" WHERE on = $1 LIMIT 1`)
	assert.Equal(t, `"c"`, tbl)
	assert.Equal(t, map[int]string{1: "on"}, cols)

	tbl, _, _ = paramColumns(`SELECT * FROM a JOIN b ON a.id = b.id WHERE b.on = $1`)
	assert.Empty(t, tbl, "a join could mean either table's column")

	// Prisma's migration bookkeeping: arithmetic on a column, and ANY.
	tbl, cols, anys := paramColumns(`UPDATE "_prisma_migrations" SET "applied_steps_count" = ("applied_steps_count" + $1) WHERE "id" = $2 OR "id" = ANY ( $3 )`)
	assert.Equal(t, `"_prisma_migrations"`, tbl)
	assert.Equal(t, map[int]string{1: "applied_steps_count", 2: "id"}, cols)
	assert.Equal(t, map[int]string{3: "id"}, anys)
}

func TestParamOID(t *testing.T) {
	for decl, oid := range map[string]uint32{
		"TEXT": oidText, "VARCHAR(36)": oidVarchar, "INTEGER": oidInt8, "bigint": oidInt8, "BOOLEAN": oidBool,
		"TIMESTAMPTZ": oidTimestamptz, "TIMESTAMP(3)": oidTimestamp, "DATE": oidDate,
		"DOUBLE PRECISION": oidFloat8, "UUID": oidUUID, "JSONB": oidJSONB,
		// Types whose binary form is not read back stay unspecified.
		"": 0, "text[]": 0, "NUMERIC(10,2) DECIMALTEXT": 0, "BYTEA": 0, "hstore": 0, "INTERVAL": 0, "POINT": 0, "mood": 0,
	} {
		assert.Equal(t, oid, paramOID(decl), decl)
	}
}
