package postgres_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestDeclaredByteaRoundTrip(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()
	_, err := conn.Exec(ctx, "CREATE TABLE pictures (id integer, picture bytea)")
	require.NoError(t, err)
	want := []byte{0, 0xff, 0x5c, 0x31}
	_, err = conn.Exec(ctx, "INSERT INTO pictures VALUES ($1, $2)", 1, want)
	require.NoError(t, err)
	// Rows written as hex text (including before this fix) must read identically.
	_, err = conn.Exec(ctx, `INSERT INTO pictures VALUES (2, '\x00ff5c31')`)
	require.NoError(t, err)
	for _, format := range []int16{pgx.TextFormatCode, pgx.BinaryFormatCode} {
		rows, err := conn.Query(ctx, "SELECT picture FROM pictures ORDER BY id", pgx.QueryResultFormats{format})
		require.NoError(t, err)
		require.Equal(t, uint32(17), rows.FieldDescriptions()[0].DataTypeOID)
		n := 0
		for rows.Next() {
			var value []byte
			require.NoError(t, rows.Scan(&value))
			require.Equal(t, want, value)
			n++
		}
		require.NoError(t, rows.Err())
		require.Equal(t, 2, n)
	}
}
