package postgres_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeHBA(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	t.Setenv("OVERLITE_HBA_DIR", dir)
}

func dial(t *testing.T, addr, pw string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	url := fmt.Sprintf("postgres://postgres@%s/test?sslmode=disable", addr)
	if pw != "" {
		url = fmt.Sprintf("postgres://postgres:%s@%s/test?sslmode=disable", pw, addr)
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	conn.Close(context.Background())
	return nil
}

// TestHBATrustAndReject: loopback is trusted, everything else rejected. The test
// client connects from 127.0.0.1, so it is trusted.
func TestHBATrustAndReject(t *testing.T) {
	writeHBA(t, "pg_hba.conf", "host all all 127.0.0.1/32 trust\nhost all all 0.0.0.0/0 reject\n")
	conn := connect(t, startServer(t))
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestHBARejectLoopback: an explicit reject for loopback refuses the connection.
func TestHBARejectLoopback(t *testing.T) {
	writeHBA(t, "pg_hba.conf", "host all all 127.0.0.1/32 reject\n")
	assert.Error(t, dial(t, startServer(t), ""), "pg_hba reject must refuse the connection")
}

// TestHBASelectsMethod: a per-connection rule picks the auth method (md5 here);
// the matching password connects, a wrong one is rejected.
func TestHBASelectsMethod(t *testing.T) {
	t.Setenv("POSTGRES_PASSWORD", "pw")
	writeHBA(t, "pg_hba.conf", "host all all 127.0.0.1/32 md5\n")
	addr := startServer(t)
	assert.NoError(t, dial(t, addr, "pw"), "correct password over md5 connects")
	assert.Error(t, dial(t, addr, "wrong"), "wrong password is rejected")
}

// TestHBAFromYAML: the YAML policy drives auth just like the conf one.
func TestHBAFromYAML(t *testing.T) {
	writeHBA(t, "pg_hba.yaml",
		"hba:\n  - {type: host, database: all, user: all, address: 127.0.0.1/32, method: trust}\n"+
			"  - {type: host, database: all, user: all, address: 0.0.0.0/0, method: reject}\n")
	conn := connect(t, startServer(t))
	var n int
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT 1`).Scan(&n))
	assert.Equal(t, 1, n)
}
