package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Prisma applies a migration file as a single Query message; this is the
// shape of one, comments over every statement included.
const migrationBatch = `-- CreateSchema
CREATE SCHEMA IF NOT EXISTS "public";

-- CreateTable
CREATE TABLE "School" (
    "id" TEXT NOT NULL,
    "name" TEXT NOT NULL,
    CONSTRAINT "School_pkey" PRIMARY KEY ("id")
);

-- CreateTable
CREATE TABLE "User" (
    "id" TEXT NOT NULL,
    "schoolId" TEXT NOT NULL,
    CONSTRAINT "User_pkey" PRIMARY KEY ("id")
);

-- AddForeignKey
ALTER TABLE "User" ADD CONSTRAINT "User_schoolId_fkey" FOREIGN KEY ("schoolId") REFERENCES "School"("id") ON DELETE CASCADE ON UPDATE CASCADE;
`

func TestMultiStatementQueryRunsEveryStatement(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, migrationBatch)

	mustExec(t, conn, `INSERT INTO "School" VALUES ('s', 'x')`)
	mustExec(t, conn, `INSERT INTO "User" VALUES ('u', 's')`)
	_, err := conn.Exec(ctx, `INSERT INTO "User" VALUES ('v', 'missing')`)
	assert.Error(t, err, "the foreign key from the batch must hold")
	mustExec(t, conn, `DELETE FROM "School"`)
	var n int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM "User"`).Scan(&n))
	assert.Equal(t, 0, n, "ON DELETE CASCADE from the batch must apply")
}

func TestMultiStatementQueryReturnsEachResult(t *testing.T) {
	conn := connect(t, startServer(t))

	results, err := conn.PgConn().Exec(context.Background(), `SELECT 1 AS a; SELECT 'x;y' AS b, 2 AS c`).ReadAll()
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, [][]byte{[]byte("1")}, results[0].Rows[0])
	assert.Equal(t, [][]byte{[]byte("x;y"), []byte("2")}, results[1].Rows[0])
}

func TestMultiStatementQueryIsOneTransaction(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, `CREATE TABLE a (x int); INSERT INTO a VALUES (1); INSERT INTO missing VALUES (1); CREATE TABLE b (x int)`)
	require.Error(t, err)

	var n int
	require.NoError(t, conn.QueryRow(ctx,
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('a', 'b')`).Scan(&n))
	assert.Equal(t, 0, n, "a failure must undo the statements before it and skip the rest")

	// The session is usable afterwards, outside any transaction.
	mustExec(t, conn, `CREATE TABLE c (x int); INSERT INTO c VALUES (1), (2)`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM c`).Scan(&n))
	assert.Equal(t, 2, n)

	// Explicit transaction control inside the batch is honored as written.
	mustExec(t, conn, `BEGIN; INSERT INTO c VALUES (3); ROLLBACK; INSERT INTO c VALUES (4)`)
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM c`).Scan(&n))
	assert.Equal(t, 3, n)
}
