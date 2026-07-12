package postgres_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColumnPrecision: information_schema.columns reports the declared length of
// character types and the precision/scale of numeric types.
func TestColumnPrecision(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx,
		`CREATE TABLE prec (a varchar(50), b numeric(10,2), c char(4), d int, e text, f numeric)`)
	require.NoError(t, err)

	type row struct {
		charLen  sql.NullInt64
		numPrec  sql.NullInt64
		numRadix sql.NullInt64
		numScale sql.NullInt64
	}
	get := func(col string) row {
		var r row
		require.NoError(t, conn.QueryRow(ctx,
			`SELECT character_maximum_length, numeric_precision, numeric_precision_radix, numeric_scale
			 FROM information_schema.columns WHERE table_name='prec' AND column_name=$1`, col).
			Scan(&r.charLen, &r.numPrec, &r.numRadix, &r.numScale))
		return r
	}

	a := get("a") // varchar(50)
	assert.Equal(t, int64(50), a.charLen.Int64)
	assert.False(t, a.numPrec.Valid)

	b := get("b") // numeric(10,2)
	assert.False(t, b.charLen.Valid)
	assert.Equal(t, int64(10), b.numPrec.Int64)
	assert.Equal(t, int64(10), b.numRadix.Int64)
	assert.Equal(t, int64(2), b.numScale.Int64)

	c := get("c") // char(4)
	assert.Equal(t, int64(4), c.charLen.Int64)

	d := get("d") // int -> nothing
	assert.False(t, d.charLen.Valid)
	assert.False(t, d.numPrec.Valid)

	f := get("f") // unqualified numeric -> no fixed precision
	assert.False(t, f.numPrec.Valid)
	assert.False(t, f.numScale.Valid)
}
