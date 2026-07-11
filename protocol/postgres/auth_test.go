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

// dialWith checks that method authenticates the right password and rejects the
// wrong one, for the auth method set in POSTGRES_HOST_AUTH_METHOD.
func authMethodCheck(t *testing.T, method string) {
	t.Setenv("POSTGRES_PASSWORD", "pw123")
	if method != "" {
		t.Setenv("POSTGRES_HOST_AUTH_METHOD", method)
	}
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
	assert.NoErrorf(t, dial("pw123"), "correct password should connect (%s)", method)
	assert.Errorf(t, dial("nope"), "wrong password should be rejected (%s)", method)
}

func TestSCRAMAuth(t *testing.T)           { authMethodCheck(t, "scram-sha-256") }
func TestMD5AuthMethod(t *testing.T)       { authMethodCheck(t, "md5") }
func TestCleartextAuthMethod(t *testing.T) { authMethodCheck(t, "password") }
func TestDefaultAuthIsSCRAM(t *testing.T)  { authMethodCheck(t, "") }
