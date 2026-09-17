package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"overlite/core"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Past the connection limit a client must be told the server is full. Waiting
// on database/sql's pool instead left it hanging with no answer at all, so the
// "too many clients" path the protocol already had was unreachable.
func TestSessionRefusesPastTheLimit(t *testing.T) {
	eng, err := Open(filepath.Join(t.TempDir(), "full.db"))
	require.NoError(t, err)
	t.Cleanup(func() { eng.Close() })

	ctx := context.Background()
	var held []core.Session
	t.Cleanup(func() {
		for _, s := range held {
			s.Close()
		}
	})

	// One connection is the engine's own, so the clients get the rest.
	for i := 0; i < maxConnections-1; i++ {
		s, err := eng.Session(ctx)
		require.NoErrorf(t, err, "session %d", i)
		held = append(held, s)
	}

	_, err = eng.Session(ctx)
	require.Error(t, err, "the session past the limit was not refused")
	assert.True(t, strings.Contains(err.Error(), "too many clients"),
		"the error should say the server is full, got: %v", err)

	// Freeing one lets the next client in.
	held[0].Close()
	held = held[1:]
	s, err := eng.Session(ctx)
	require.NoError(t, err)
	held = append(held, s)
}
