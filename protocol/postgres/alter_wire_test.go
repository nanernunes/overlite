package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

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

// TestAlterAddConstraintEnforced covers the constraints a pg_dump restore adds
// after the fact: they used to be accepted and dropped on the floor, so a
// restored database kept none of them. ONLY and the public. qualifier are how
// pg_dump writes them.
func TestAlterAddConstraintEnforced(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	mustExec(t, conn, `CREATE TABLE dept (id int, name text)`)
	mustExec(t, conn, `CREATE TABLE emp (id int, dept_id int)`)
	mustExec(t, conn, `ALTER TABLE ONLY public.dept ADD CONSTRAINT dept_pkey PRIMARY KEY (id)`)
	mustExec(t, conn, `ALTER TABLE ONLY public.dept ADD CONSTRAINT dept_name_key UNIQUE (name)`)
	mustExec(t, conn, `ALTER TABLE ONLY public.emp ADD CONSTRAINT fk_emp_0 FOREIGN KEY (dept_id) REFERENCES public.dept(id)`)

	mustExec(t, conn, `INSERT INTO dept VALUES (1, 'eng')`)

	_, err := conn.Exec(ctx, `INSERT INTO dept VALUES (1, 'other')`)
	assert.Error(t, err, "PRIMARY KEY must reject a duplicate id")
	_, err = conn.Exec(ctx, `INSERT INTO dept VALUES (2, 'eng')`)
	assert.Error(t, err, "UNIQUE must reject a duplicate name")
	_, err = conn.Exec(ctx, `INSERT INTO emp VALUES (1, 99)`)
	assert.Error(t, err, "FOREIGN KEY must reject a missing parent")

	_, err = conn.Exec(ctx, `INSERT INTO emp VALUES (1, 1)`)
	assert.NoError(t, err, "a row satisfying every constraint must go in")

	// The catalog has to report them as constraints, not as bare indexes, or
	// the next pg_dump emits an index where a constraint belongs.
	got := queryColumn(t, conn,
		`SELECT conname FROM pg_catalog.pg_constraint WHERE contype IN ('p','u','f') ORDER BY conname`, 0)
	assert.Contains(t, got, "dept_name_key")
	assert.Contains(t, got, "fk_emp_0")
}

// TestEnumColumnKeepsItsType pins the declared type of an enum column. It is
// stored as text plus a CHECK, so the catalog has to recover the enum from that
// CHECK — otherwise a dump writes `m text` and the restored column is no longer
// the enum.
func TestEnumColumnKeepsItsType(t *testing.T) {
	addr := startServer(t)
	c1 := connect(t, addr)
	ctx := context.Background()
	mustExec(t, c1, `CREATE TYPE mood AS ENUM ('sad', 'ok', 'happy')`)
	mustExec(t, c1, `CREATE TABLE t (id int, m mood, nota text)`)

	typeOf := func(c interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	}, col string) (oid uint32, name string) {
		require.NoError(t, c.QueryRow(ctx,
			`SELECT atttypid, format_type(atttypid, atttypmod) FROM pg_catalog.pg_attribute
			 WHERE attrelid = 't'::regclass AND attname = $1`, col).Scan(&oid, &name))
		return
	}

	// On the connection that created the type, whose registry was loaded before
	// the type existed...
	oid, name := typeOf(c1, "m")
	assert.Greater(t, oid, uint32(90000000), "an enum column reports the enum's oid, not text's")
	assert.Equal(t, "mood", name)

	// ...and on a client that connects afterwards, which is what pg_dump is.
	c2 := connect(t, addr)
	_, name = typeOf(c2, "m")
	assert.Equal(t, "mood", name)

	// A plain text column must not be mistaken for one.
	_, name = typeOf(c2, "nota")
	assert.Equal(t, "text", name)
}
