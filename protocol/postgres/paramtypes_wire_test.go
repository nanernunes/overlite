package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokio-postgres (Prisma's schema engine, sqlx, Diesel) sends every parameter
// in binary, by the type Describe gives it, and looks a type it does not know
// up in pg_type. Parameters went out unspecified, so the lookup's own
// parameter was unknown too, and it recursed until the client crashed. They
// are now typed by the column or catalog column they meet, and binary values
// are read back by that type.
func TestParameterTypes(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE TABLE migrations (id varchar(36) PRIMARY KEY, steps integer NOT NULL DEFAULT 0, started_at timestamptz, note text)`)

	paramOIDs := func(sql string) []uint32 {
		t.Helper()
		sd, err := conn.Prepare(ctx, "", sql)
		require.NoError(t, err)
		return sd.ParamOIDs
	}
	assert.Equal(t, []uint32{26}, paramOIDs(`SELECT t.typname, t.typtype, t.typelem, t.typbasetype, t.typrelid FROM pg_catalog.pg_type t WHERE t.oid = $1`))
	assert.Equal(t, []uint32{19}, paramOIDs(`SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname = $1)`))
	assert.Equal(t, []uint32{1003}, paramOIDs(`SELECT nspname FROM pg_namespace WHERE nspname = ANY ( $1 )`))
	assert.Equal(t, []uint32{1043, 1184, 25}, paramOIDs(`INSERT INTO "migrations" ("id","started_at","note") VALUES ($1,$2,$3)`))
	assert.Equal(t, []uint32{20, 1043}, paramOIDs(`UPDATE "migrations" SET "steps" = ("steps" + $1) WHERE "id" = $2`))

	// Binary values, read by their type: a timestamptz is stored as bare UTC,
	// and a four-byte string stays a string rather than becoming an integer.
	started := time.Date(2026, 10, 18, 9, 30, 0, 123456000, time.FixedZone("", -3*3600))
	_, err := conn.Exec(ctx, `INSERT INTO "migrations" ("id","started_at","note") VALUES ($1,$2,$3)`, "abcd", started, "note")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `UPDATE "migrations" SET "steps" = ("steps" + $1) WHERE "id" = $2`, 2, "abcd")
	require.NoError(t, err)

	var id string
	var steps int
	var at time.Time
	require.NoError(t, conn.QueryRow(ctx, `SELECT id, steps, started_at FROM migrations`).Scan(&id, &steps, &at))
	assert.Equal(t, "abcd", id)
	assert.Equal(t, 2, steps)
	assert.True(t, started.Equal(at), "started_at %v", at)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM migrations WHERE started_at = '2026-10-18 12:30:00.123456+00'`).Scan(&n))
	assert.Equal(t, 1, n, "stored as the same literal would be")

	// A type the client declares in Parse is kept, and its binary value read by it.
	res := conn.PgConn().ExecParams(ctx, `SELECT $1 || '!'`, [][]byte{[]byte("wxyz")}, []uint32{25}, []int16{1}, nil).Read()
	require.NoError(t, res.Err)
	require.Len(t, res.Rows, 1)
	assert.Equal(t, "wxyz!", string(res.Rows[0][0]))
}
