package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Rows are encoded in the types Describe advertised. Describe types a computed
// column by a probe run of the statement; when that run finds no rows it can
// only say text, and the rows of the real run used to go out as binary
// integers under that text description.
func TestRowsFollowDescribedTypes(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY, name text)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `INSERT INTO t VALUES (7, 'a')`)
	require.NoError(t, err)

	// Binary results, as tokio-postgres (Prisma's driver) asks for them.
	var n string
	require.NoError(t, conn.QueryRow(ctx, `SELECT id + 0 AS n FROM t WHERE name = $1`,
		pgx.QueryResultFormats{pgx.BinaryFormatCode}, "a").Scan(&n))
	assert.Equal(t, "7", n)
}

// The integer columns of a catalog query are described as integers even when
// the probe run finds nothing, as Prisma's introspection needs them.
func TestCatalogIntegersDescribed(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE p (id int PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = conn.Exec(ctx, `CREATE TABLE c (id int PRIMARY KEY, p_id int REFERENCES p (id))`)
	require.NoError(t, err)

	var conID, child int64
	require.NoError(t, conn.QueryRow(ctx, `SELECT con.oid AS con_id, k.attnum AS child
		FROM pg_constraint con
		JOIN pg_namespace ns ON ns.oid = con.connamespace
		JOIN pg_attribute k ON k.attrelid = con.conrelid AND k.attname = 'p_id'
		WHERE ns.nspname = ANY ($1) AND con.contype = 'f'`, []string{"public"}).Scan(&conID, &child))
	assert.Positive(t, conID)
	assert.Equal(t, int64(2), child)
}
