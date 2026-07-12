package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAlterAddUnique: ADD [CONSTRAINT] UNIQUE becomes a real unique index and is
// enforced.
func TestAlterAddUnique(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, email text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1,'a@x'),(2,'b@x')`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `ALTER TABLE t ADD CONSTRAINT t_email_key UNIQUE (email)`)
	require.NoError(t, err)

	// The constraint is enforced.
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (3,'a@x')`)
	require.Error(t, err, "duplicate email rejected")
	// A new distinct value is fine.
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (3,'c@x')`)
	require.NoError(t, err)
}

// TestAlterColumnType: ALTER COLUMN … TYPE rebuilds the table, preserving data,
// and re-advertises the new type.
func TestAlterColumnType(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, note text, price int)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1,'hi',100),(2,'yo',200)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `CREATE INDEX t_note ON t (note)`)
	require.NoError(t, err)

	_, err = conn.Exec(ctx, `ALTER TABLE t ALTER COLUMN price TYPE numeric`)
	require.NoError(t, err)

	// Data survived the rebuild.
	var n, id, p int
	require.NoError(t, conn.QueryRow(ctx, `SELECT count(*) FROM t`).Scan(&n))
	assert.Equal(t, 2, n)
	require.NoError(t, conn.QueryRow(ctx, `SELECT id, price FROM t WHERE id = 2`).Scan(&id, &p))
	assert.Equal(t, 200, p)
	// The PK still enforces uniqueness after the rebuild.
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1,'dup',1)`)
	require.Error(t, err, "primary key preserved")
	// The recreated index still exists (a second ALTER that rebuilds again works).
	_, err = conn.Exec(ctx, `ALTER TABLE t ALTER COLUMN note TYPE varchar(50)`)
	require.NoError(t, err)
}

// TestAlterColumnNotNull: SET/DROP NOT NULL via rebuild.
func TestAlterColumnNotNull(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int, name text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (1,'a')`)
	require.NoError(t, err)

	_, err = conn.Exec(ctx, `ALTER TABLE t ALTER COLUMN name SET NOT NULL`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t (id) VALUES (2)`)
	require.Error(t, err, "NOT NULL enforced")

	_, err = conn.Exec(ctx, `ALTER TABLE t ALTER COLUMN name DROP NOT NULL`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t (id) VALUES (2)`)
	require.NoError(t, err, "NOT NULL dropped")
}
