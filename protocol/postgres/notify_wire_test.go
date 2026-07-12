package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestListenNotify: a session that LISTENs on a channel receives a NOTIFY sent by
// another session on the same server (real in-memory delivery).
func TestListenNotify(t *testing.T) {
	addr := startServer(t)
	listener := connect(t, addr)
	notifier := connect(t, addr)
	ctx := context.Background()

	_, err := listener.Exec(ctx, `LISTEN chan1`)
	require.NoError(t, err)

	// Notify from the other connection, with a payload.
	_, err = notifier.Exec(ctx, `NOTIFY chan1, 'hello world'`)
	require.NoError(t, err)

	wctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	n, err := listener.WaitForNotification(wctx)
	require.NoError(t, err)
	assert.Equal(t, "chan1", n.Channel)
	assert.Equal(t, "hello world", n.Payload)

	// UNLISTEN stops delivery: a following NOTIFY must not arrive.
	_, err = listener.Exec(ctx, `UNLISTEN chan1`)
	require.NoError(t, err)
	_, err = notifier.Exec(ctx, `NOTIFY chan1, 'ignored'`)
	require.NoError(t, err)
	wctx2, cancel2 := context.WithTimeout(ctx, 400*time.Millisecond)
	defer cancel2()
	_, err = listener.WaitForNotification(wctx2)
	require.Error(t, err, "no notification after UNLISTEN")
}
