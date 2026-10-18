package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An ALTER TABLE with several actions applies them all, or none when one fails.
func TestAlterTableSeveralActions(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE "School" ("id" TEXT NOT NULL PRIMARY KEY)`)

	mustExec(t, conn, "-- AlterTable\nALTER TABLE \"School\" ADD COLUMN     \"nickname\" TEXT,\n"+
		"ADD COLUMN     \"rank\" INTEGER NOT NULL DEFAULT 0;")
	assert.Equal(t, []string{"id", "nickname", "rank"}, queryColumn(t, conn,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'School' ORDER BY ordinal_position`, 0))

	_, err := conn.Exec(context.Background(), `ALTER TABLE "School" ADD COLUMN "city" TEXT, ADD COLUMN "rank" INTEGER`)
	require.Error(t, err)
	assert.Equal(t, []string{"id", "nickname", "rank"}, queryColumn(t, conn,
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'School' ORDER BY ordinal_position`, 0),
		"the first action must be undone with the failed second")
}
