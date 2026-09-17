package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A schema file is attached, and the connection string's pragmas only reach the
// database opened with it — so every tenant file kept SQLite's default rollback
// journal, where one writer locks the whole file. Two tenants writing at once
// collapsed onto the busy timeout and then raised SQLITE_BUSY.

func TestAttachedSchemaFilesAreWAL(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	conn := connect(t, startServer(t))
	ctx := context.Background()

	mustExec(t, conn, `CREATE SCHEMA acme`)
	mustExec(t, conn, `CREATE TABLE "acme"."t" (id int primary key)`)

	var mode string
	require.NoError(t, conn.QueryRow(ctx, `SELECT * FROM pragma_journal_mode('acme')`).Scan(&mode))
	assert.Equal(t, "wal", mode, "the schema file is not in WAL mode")

}

// Two tenants writing at the same time must not serialise onto each other.
func TestConcurrentTenantWrites(t *testing.T) {
	t.Setenv("OVERLITE_MULTITENANT_SCHEMA", "true")
	addr := startServer(t)
	ctx := context.Background()

	setup := connect(t, addr)
	for _, s := range []string{"acme", "globex"} {
		mustExec(t, setup, `CREATE SCHEMA `+s)
		mustExec(t, setup, fmt.Sprintf(`CREATE TABLE %q."t" (id integer primary key autoincrement, v text)`, s))
	}

	const perTenant = 40
	var wg sync.WaitGroup
	errs := make(chan error, 2*perTenant)
	for _, s := range []string{"acme", "globex"} {
		wg.Add(1)
		go func(schema string) {
			defer wg.Done()
			c := connect(t, addr)
			for i := 0; i < perTenant; i++ {
				if _, err := c.Exec(ctx,
					fmt.Sprintf(`INSERT INTO %q."t" (v) VALUES ($1)`, schema), "x"); err != nil {
					errs <- err
					return
				}
			}
		}(s)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("a tenant's write failed while another was writing: %v", err)
	}

	for _, s := range []string{"acme", "globex"} {
		var n int
		require.NoError(t, setup.QueryRow(ctx, fmt.Sprintf(`SELECT count(*) FROM %q."t"`, s)).Scan(&n))
		assert.Equalf(t, perTenant, n, "%s lost writes", s)
	}
}
