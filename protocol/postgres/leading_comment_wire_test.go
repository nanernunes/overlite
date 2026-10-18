package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Migration tools write a comment above every statement: Prisma's migrations
// read `-- CreateSchema` over CREATE SCHEMA and `-- AddForeignKey` over each
// ALTER TABLE … ADD CONSTRAINT. The statements overlite handles itself are
// recognized by their first words, so a comment in front of them used to send
// them straight to SQLite, which rejects both.

func TestLeadingCommentKeepsStatementRouting(t *testing.T) {
	for name, conn := range map[string]*pgx.Conn{
		"simple":   connect(t, startServer(t)),
		"extended": connectExtended(t, startServer(t)),
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			mustExec(t, conn, "-- CreateSchema\nCREATE SCHEMA IF NOT EXISTS \"public\"")
			mustExec(t, conn, "/* tenants */ CREATE SCHEMA sales")
			mustExec(t, conn, `CREATE TABLE "Parent" (id TEXT PRIMARY KEY)`)
			mustExec(t, conn, `CREATE TABLE "Child" (id TEXT PRIMARY KEY, "parentId" TEXT)`)
			mustExec(t, conn, "-- AddForeignKey\n"+
				`ALTER TABLE "Child" ADD CONSTRAINT "Child_parentId_fkey" FOREIGN KEY ("parentId") `+
				`REFERENCES "Parent"("id") ON DELETE CASCADE ON UPDATE CASCADE`)

			_, err := conn.Exec(ctx, `INSERT INTO "Child" VALUES ('c', 'missing')`)
			assert.Error(t, err, "the foreign key must hold")

			mustExec(t, conn, `INSERT INTO "Parent" VALUES ('p')`)
			mustExec(t, conn, `INSERT INTO "Child" VALUES ('c', 'p')`)
			mustExec(t, conn, "-- cascade\nDELETE FROM \"Parent\"")
			var n int
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "Child"`).Scan(&n))
			assert.Equal(t, 0, n, "ON DELETE CASCADE must remove the child")

			// Transaction control is routed the same way.
			mustExec(t, conn, "-- start\nBEGIN")
			mustExec(t, conn, `INSERT INTO "Parent" VALUES ('q')`)
			mustExec(t, conn, "-- undo\nROLLBACK")
			require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "Parent"`).Scan(&n))
			assert.Equal(t, 0, n, "the commented ROLLBACK must undo the insert")
		})
	}
}
