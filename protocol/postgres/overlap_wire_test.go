package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRangeOverlapAndContainment: the "&&" overlap operator and range @> range
// containment over the text-range representation.
func TestRangeOverlapAndContainment(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	q := func(sql string) int {
		var n int
		require.NoError(t, conn.QueryRow(ctx, sql).Scan(&n))
		return n
	}

	// Range overlap.
	assert.Equal(t, 1, q(`SELECT int4range(1,10) && int4range(5,15)`))
	assert.Equal(t, 0, q(`SELECT int4range(1,5) && int4range(5,10)`))       // [1,5) vs [5,10): touch, no overlap
	assert.Equal(t, 1, q(`SELECT int4range(1,5,'[]') && int4range(5,10)`))  // [1,5] includes 5
	assert.Equal(t, 1, q(`SELECT numrange(NULL, 10) && numrange(3, NULL)`)) // (-inf,10) & (3,+inf)

	// Range contains range.
	assert.Equal(t, 1, q(`SELECT int4range(1,20) @> int4range(5,10)`))
	assert.Equal(t, 0, q(`SELECT int4range(1,10) @> int4range(5,20)`))
	assert.Equal(t, 1, q(`SELECT int4range(1,10) @> 5`)) // range @> element still works

	// Array overlap.
	assert.Equal(t, 1, q(`SELECT ARRAY[1,2,3] && ARRAY[3,4,5]`))
	assert.Equal(t, 0, q(`SELECT ARRAY[1,2] && ARRAY[3,4]`))
}
