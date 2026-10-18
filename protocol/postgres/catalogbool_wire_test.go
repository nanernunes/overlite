package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A catalog boolean compared with a string literal behaves as in Postgres, and
// the booleans introspection selects come back typed bool, not 0/1.
func TestCatalogBooleans(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, `CREATE TABLE t (id int PRIMARY KEY)`)
	require.NoError(t, err)

	// Prisma's table listing matched no table while relispartition = 'f'
	// compared 0 with 'f'.
	var name string
	var isPartition, hasSubclass bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT tbl.relname,
		(tbl.relhassubclass and tbl.relkind = 'p') as is_partition,
		(tbl.relhassubclass and tbl.relkind = 'r') as has_subclass
		FROM pg_class AS tbl
		WHERE (tbl.relkind = 'r' AND tbl.relispartition = 'f') OR tbl.relkind = 'p'`).
		Scan(&name, &isPartition, &hasSubclass))
	assert.Equal(t, "t", name)
	assert.False(t, isPartition)
	assert.False(t, hasSubclass)

	var unique, primary bool
	require.NoError(t, conn.QueryRow(ctx, `SELECT indisunique AS is_unique, indisprimary AS is_primary_key
		FROM pg_index WHERE indisprimary = 't'`).Scan(&unique, &primary))
	assert.True(t, unique)
	assert.True(t, primary)
}
