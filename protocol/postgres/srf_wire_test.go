package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Set-returning calls in a select list run in lockstep, one row per element,
// over stored arrays and over the catalog's vectors alike.
func TestSelectListSRFs(t *testing.T) {
	conn := connect(t, startServer(t))
	mustExec(t, conn, `CREATE TABLE t (id int PRIMARY KEY, a text[], b text[])`)
	mustExec(t, conn, `INSERT INTO t VALUES (1, ARRAY['x','y'], ARRAY['p','q'])`)

	assert.Equal(t, []string{"1:x:p", "2:y:q"}, queryColumn(t, conn,
		`SELECT generate_subscripts(a, 1) || ':' || unnest(a) || ':' || unnest(b) FROM t`, 0))

	// int2vector subscripts start at 0, as Postgres numbers them.
	mustExec(t, conn, `CREATE TABLE parent (x int, y int, PRIMARY KEY (x, y))`)
	assert.Equal(t, []string{"0:1", "1:2"}, queryColumn(t, conn, `SELECT i || ':' || k FROM (
		SELECT unnest(indkey) AS k, generate_subscripts(indkey, 1) AS i
		FROM pg_index WHERE indrelid = (SELECT oid FROM pg_class WHERE relname = 'parent') ORDER BY i) s`, 0))
}
