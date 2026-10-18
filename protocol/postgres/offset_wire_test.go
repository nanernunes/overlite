package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prisma ends nearly every read with a bare `OFFSET $n` (no LIMIT), which
// Postgres accepts and SQLite rejects as a syntax error.

func TestOffsetWithoutLimit(t *testing.T) {
	for name, conn := range map[string]*pgx.Conn{
		"simple":   connect(t, startServer(t)),
		"extended": connectExtended(t, startServer(t)),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			mustExec(t, conn, `CREATE TABLE "User" (id int PRIMARY KEY, role text)`)
			mustExec(t, conn, `INSERT INTO "User" VALUES (1, 'a'), (2, 'a'), (3, 'b'), (4, 'a')`)

			ids := func(sql string, args ...any) []int {
				rows, err := conn.Query(ctx, sql, args...)
				require.NoError(t, err, sql)
				got, err := pgx.CollectRows(rows, pgx.RowTo[int])
				require.NoError(t, err, sql)
				return got
			}

			assert.Equal(t, []int{2, 4},
				ids(`SELECT "id" FROM "User" WHERE "role" IN ($1) ORDER BY "id" OFFSET $2`, "a", 1))
			assert.Equal(t, []int{3},
				ids(`SELECT COUNT(*) FROM (SELECT "id" FROM "User" WHERE 1=1 OFFSET $1) AS "sub"`, 1))
			assert.Equal(t, []int{2, 3},
				ids(`SELECT id FROM "User" ORDER BY id OFFSET 1 LIMIT 2`))
		})
	}
}
