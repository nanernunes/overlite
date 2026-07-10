package postgres_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These cover the session/transaction/SHOW statements a JDBC driver fires
// during connection setup — the difference between DBeaver connecting or not.

func TestUtilitySimpleProtocol(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	// SET (what pgjdbc sends on connect) must succeed as a no-op.
	_, err := conn.Exec(ctx, "SET extra_float_digits = 3")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "SET application_name = 'DBeaver'")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "RESET ALL")
	require.NoError(t, err)

	// Transaction control is accepted (no-op today).
	_, err = conn.Exec(ctx, "BEGIN")
	require.NoError(t, err)
	_, err = conn.Exec(ctx, "COMMIT")
	require.NoError(t, err)
}

func TestUtilityShow(t *testing.T) {
	conn := connect(t, startServer(t))
	ctx := context.Background()

	var searchPath string
	require.NoError(t, conn.QueryRow(ctx, "SHOW search_path").Scan(&searchPath))
	assert.Contains(t, searchPath, "public")

	var iso string
	require.NoError(t, conn.QueryRow(ctx, "SHOW TRANSACTION ISOLATION LEVEL").Scan(&iso))
	assert.Equal(t, "read committed", iso)

	var scs string
	require.NoError(t, conn.QueryRow(ctx, "SHOW standard_conforming_strings").Scan(&scs))
	assert.Equal(t, "on", scs)
}

func TestUtilityExtendedProtocol(t *testing.T) {
	// The same statements through the extended protocol (Parse/Bind/Describe/
	// Execute), which pgjdbc uses for most queries.
	conn := connectExtended(t, startServer(t))
	ctx := context.Background()

	_, err := conn.Exec(ctx, "SET extra_float_digits = 3")
	require.NoError(t, err)

	var v string
	require.NoError(t, conn.QueryRow(ctx, "SHOW server_version").Scan(&v))
	assert.Equal(t, "15.0", v)
}

func TestUtilityCurrentSetting(t *testing.T) {
	conn := connectExtended(t, startServer(t))
	var v string
	require.NoError(t, conn.QueryRow(context.Background(),
		"SELECT current_setting('standard_conforming_strings')").Scan(&v))
	assert.Equal(t, "on", v)
}
