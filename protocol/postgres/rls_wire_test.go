package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rlsSetup creates a multi-tenant docs table under RLS: each role sees only the
// rows whose owner column equals current_user.
func rlsSetup(t *testing.T) (addr string) {
	t.Helper()
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr = startServer(t)
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `CREATE ROLE bob LOGIN PASSWORD 'b'`)
	mustExec(t, admin, `CREATE TABLE docs (id int, owner text, body text)`)
	mustExec(t, admin, `INSERT INTO docs VALUES (1,'alice','a1'),(2,'bob','b1'),(3,'alice','a2')`)
	mustExec(t, admin, `GRANT ALL ON docs TO alice`)
	mustExec(t, admin, `GRANT ALL ON docs TO bob`)
	mustExec(t, admin, `ALTER TABLE docs ENABLE ROW LEVEL SECURITY`)
	mustExec(t, admin, `CREATE POLICY own ON docs FOR ALL USING (owner = current_user)`)
	return addr
}

// TestRLSSelect: a subject role sees only its own rows; the superuser owner sees
// everything.
func TestRLSSelect(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()

	alice := connectAs(t, addr, "alice", "a")
	var n int
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM docs`).Scan(&n))
	assert.Equal(t, 2, n, "alice sees her 2 rows")

	// A filter on top of RLS still works.
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM docs WHERE id > 1`).Scan(&n))
	assert.Equal(t, 1, n)

	bob := connectAs(t, addr, "bob", "b")
	require.NoError(t, bob.QueryRow(ctx, `SELECT count(*) FROM docs`).Scan(&n))
	assert.Equal(t, 1, n, "bob sees his 1 row")

	// Superuser owner bypasses RLS.
	admin := connectAs(t, addr, "postgres", "adminpw")
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM docs`).Scan(&n))
	assert.Equal(t, 3, n)
}

// TestRLSUpdateDelete: USING limits which rows a subject may change.
func TestRLSUpdateDelete(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()
	alice := connectAs(t, addr, "alice", "a")

	// alice can't delete bob's row (id 2 invisible under USING).
	ct, err := alice.Exec(ctx, `DELETE FROM docs WHERE id = 2`)
	require.NoError(t, err)
	assert.Equal(t, int64(0), ct.RowsAffected(), "bob's row not visible")

	// alice can delete her own.
	ct, err = alice.Exec(ctx, `DELETE FROM docs WHERE id = 1`)
	require.NoError(t, err)
	assert.Equal(t, int64(1), ct.RowsAffected())

	// alice can't update bob's row.
	ct, err = alice.Exec(ctx, `UPDATE docs SET body = 'hacked' WHERE id = 2`)
	require.NoError(t, err)
	assert.Equal(t, int64(0), ct.RowsAffected())

	// Confirm via superuser that bob's row is intact.
	admin := connectAs(t, addr, "postgres", "adminpw")
	var body string
	require.NoError(t, admin.QueryRow(ctx, `SELECT body FROM docs WHERE id = 2`).Scan(&body))
	assert.Equal(t, "b1", body)
}

// TestRLSInsertWithCheck: FOR ALL USING doubles as the write check; a row that
// fails it is rejected.
func TestRLSInsertWithCheck(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()
	alice := connectAs(t, addr, "alice", "a")

	// Inserting a row she owns is fine.
	_, err := alice.Exec(ctx, `INSERT INTO docs VALUES (10, 'alice', 'mine')`)
	require.NoError(t, err)

	// Inserting a row owned by someone else violates the policy.
	_, err = alice.Exec(ctx, `INSERT INTO docs (id, owner, body) VALUES (11, 'bob', 'nope')`)
	require.Error(t, err, "row violates WITH CHECK")

	// The good row persisted, the bad one didn't.
	admin := connectAs(t, addr, "postgres", "adminpw")
	var n int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM docs WHERE id IN (10,11)`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestRLSInsertParameterized: WITH CHECK is enforced for parameterized inserts
// (extended protocol), where the values arrive as bound params.
func TestRLSInsertParameterized(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()
	alice := connectAs(t, addr, "alice", "a")

	// A row she owns, passed as parameters, is accepted.
	_, err := alice.Exec(ctx, `INSERT INTO docs VALUES ($1, $2, $3)`, 20, "alice", "p")
	require.NoError(t, err)

	// A row owned by someone else is rejected even via params.
	_, err = alice.Exec(ctx, `INSERT INTO docs (id, owner, body) VALUES ($1, $2, $3)`, 21, "bob", "p")
	require.Error(t, err, "parameterized row violates WITH CHECK")

	admin := connectAs(t, addr, "postgres", "adminpw")
	var n int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM docs WHERE id IN (20,21)`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestRLSInsertReturning: WITH CHECK is enforced even when the INSERT carries a
// RETURNING clause, and the returned row still comes back on success.
func TestRLSInsertReturning(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()
	alice := connectAs(t, addr, "alice", "a")

	// Own row: accepted, and RETURNING yields it.
	var id int
	require.NoError(t, alice.QueryRow(ctx,
		`INSERT INTO docs VALUES (40, 'alice', 'r') RETURNING id`).Scan(&id))
	assert.Equal(t, 40, id)

	// Parameterized RETURNING, own row.
	require.NoError(t, alice.QueryRow(ctx,
		`INSERT INTO docs VALUES ($1, $2, $3) RETURNING id`, 41, "alice", "r").Scan(&id))
	assert.Equal(t, 41, id)

	// Foreign row is rejected despite RETURNING.
	err := alice.QueryRow(ctx,
		`INSERT INTO docs VALUES (42, 'bob', 'r') RETURNING id`).Scan(&id)
	require.Error(t, err)

	admin := connectAs(t, addr, "postgres", "adminpw")
	var n int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM docs WHERE id IN (40,41,42)`).Scan(&n))
	assert.Equal(t, 2, n)
}

// TestRLSInsertSelect: WITH CHECK is enforced on INSERT … SELECT — every row
// the source produces must satisfy the policy.
func TestRLSInsertSelect(t *testing.T) {
	addr := rlsSetup(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE TABLE incoming (id int, owner text, body text)`)
	mustExec(t, admin, `INSERT INTO incoming VALUES (50,'alice','s'),(51,'bob','s')`)
	mustExec(t, admin, `GRANT SELECT ON incoming TO alice`)

	alice := connectAs(t, addr, "alice", "a")

	// Source row she owns: accepted.
	_, err := alice.Exec(ctx, `INSERT INTO docs SELECT * FROM incoming WHERE id = 50`)
	require.NoError(t, err)

	// A source row owned by someone else is rejected.
	_, err = alice.Exec(ctx, `INSERT INTO docs SELECT * FROM incoming WHERE id = 51`)
	require.Error(t, err, "row violates WITH CHECK")

	// A batch containing a bad row rejects the whole statement.
	_, err = alice.Exec(ctx, `INSERT INTO docs SELECT * FROM incoming`)
	require.Error(t, err)

	// Only the good row landed.
	var n int
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM docs WHERE id IN (50,51)`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestRLSDefaultDeny: with RLS enabled and no policy for the command, a subject
// sees nothing.
func TestRLSDefaultDeny(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "adminpw")
	addr := startServer(t)
	ctx := context.Background()
	admin := connectAs(t, addr, "postgres", "adminpw")
	mustExec(t, admin, `CREATE ROLE alice LOGIN PASSWORD 'a'`)
	mustExec(t, admin, `CREATE TABLE t (id int)`)
	mustExec(t, admin, `INSERT INTO t VALUES (1),(2)`)
	mustExec(t, admin, `GRANT ALL ON t TO alice`)
	mustExec(t, admin, `ALTER TABLE t ENABLE ROW LEVEL SECURITY`) // no policy at all

	alice := connectAs(t, addr, "alice", "a")
	var n int
	require.NoError(t, alice.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 0, n, "default deny")

	// Superuser still bypasses.
	require.NoError(t, admin.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)
}
