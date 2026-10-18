package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A DROP DATABASE waits for a client that is still leaving the database, as
// Postgres waits for its backend to exit. Prisma drops its shadow database the
// moment it lets go of it.
func TestDropDatabaseWaitsForLeavingClient(t *testing.T) {
	addr, _ := startCluster(t, 8)
	admin := dialDB(t, addr, entryDatabase)
	require.NotNil(t, admin)
	shadow := mustCreateDatabase(t, addr, admin, "shadow")

	go func() {
		time.Sleep(200 * time.Millisecond)
		shadow.Close(context.Background())
	}()
	mustExec(t, admin, `DROP DATABASE IF EXISTS "shadow" WITH (FORCE)`)
	assert.Nil(t, dialDB(t, addr, "shadow"))
}

// Each way a CREATE/DROP DATABASE fails has its own SQLSTATE.
func TestDatabaseDDLErrorCodes(t *testing.T) {
	addr, _ := startCluster(t, 8)
	admin := dialDB(t, addr, entryDatabase)
	require.NotNil(t, admin)
	mustExec(t, admin, `CREATE DATABASE shop`)

	code := func(sql string) string {
		_, err := admin.Exec(context.Background(), sql)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "%s: want a PgError, got %v", sql, err)
		return pgErr.Code
	}
	assert.Equal(t, "42P04", code(`CREATE DATABASE shop`))
	assert.Equal(t, "3D000", code(`DROP DATABASE nope`))
	assert.Equal(t, "55006", code(`DROP DATABASE `+entryDatabase))
}
