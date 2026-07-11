package postgres_test

import (
	"context"
	"crypto/tls"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// With TLS enabled (self-signed), a client using sslmode=require negotiates an
// encrypted connection, while a plaintext client (sslmode=disable) still works.
func TestTLSConnection(t *testing.T) {
	t.Setenv("POSTGRES_SSL", "on")
	addr := startServer(t) // postgres.New() reads POSTGRES_SSL -> self-signed TLS
	ctx := context.Background()

	// sslmode=require: encrypt, don't verify the (self-signed) cert.
	cfg, err := pgx.ParseConfig(fmt.Sprintf("postgres://postgres@%s/test?sslmode=require", addr))
	require.NoError(t, err)
	secure, err := pgx.ConnectConfig(ctx, cfg)
	require.NoError(t, err)
	defer secure.Close(ctx)

	_, isTLS := secure.PgConn().Conn().(*tls.Conn)
	assert.True(t, isTLS, "sslmode=require must negotiate TLS")

	var n int
	require.NoError(t, secure.QueryRow(ctx, "SELECT 1").Scan(&n))
	assert.Equal(t, 1, n)

	// A plaintext client (no SSLRequest) is still accepted.
	plain := connect(t, addr)
	require.NoError(t, plain.QueryRow(ctx, "SELECT 2").Scan(&n))
	assert.Equal(t, 2, n)
}
