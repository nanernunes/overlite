package postgres_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
)

// With POSTGRES_PASSWORD set, clients must present the matching password;
// without it, connections are trusted (covered by every other test).
func TestPasswordAuth(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "s3cr3t")
	addr := startServer(t) // startServer's postgres.New() reads the env

	dial := func(pw string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx,
			fmt.Sprintf("postgres://postgres:%s@%s/test?sslmode=disable", pw, addr))
		if err != nil {
			return err
		}
		conn.Close(context.Background())
		return nil
	}

	assert.NoError(t, dial("s3cr3t"), "correct password should connect")
	assert.Error(t, dial("wrong"), "wrong password should be rejected")
}

// TestCleartextAuthMethod forces cleartext auth via POSTGRES_HOST_AUTH_METHOD.
func TestCleartextAuthMethod(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "pw123")
	t.Setenv("POSTGRES_HOST_AUTH_METHOD", "password")
	addr := startServer(t)

	dial := func(pw string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		conn, err := pgx.Connect(ctx,
			fmt.Sprintf("postgres://postgres:%s@%s/test?sslmode=disable", pw, addr))
		if err != nil {
			return err
		}
		conn.Close(context.Background())
		return nil
	}

	assert.NoError(t, dial("pw123"), "correct password should connect (cleartext)")
	assert.Error(t, dial("nope"), "wrong password should be rejected (cleartext)")
}
