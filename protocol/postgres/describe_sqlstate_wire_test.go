package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A statement that fails to prepare reports the code it would fail with at
// execution: a missing table is 42P01 whether the client prepares first or not.
func TestDescribeErrorCode(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	_, err := conn.Exec(context.Background(), `SELECT * FROM nope WHERE a = $1`, 1)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "want a PgError, got %v", err)
	assert.Equal(t, "42P01", pgErr.Code)
}
