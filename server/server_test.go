package server

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"overlite/core"
)

// panicProto panics on the first connection and serves the second normally, so
// the test can prove the first did not take the process (or the listener) down.
type panicProto struct{ seen int }

func (p *panicProto) Name() string     { return "panic-test" }
func (p *panicProto) DefaultPort() int { return 0 }

func (p *panicProto) Serve(_ context.Context, conn net.Conn, _ core.Cluster) error {
	p.seen++
	if p.seen == 1 {
		panic("boom")
	}
	_, _ = conn.Write([]byte("ok"))
	return nil
}

// TestHandleContainsPanics pins the guarantee that one client cannot end the
// server for everyone else.
func TestHandleContainsPanics(t *testing.T) {
	proto := &panicProto{}
	srv, err := New("127.0.0.1:0", proto, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.Serve(ctx); err != nil && !errors.Is(err, net.ErrClosed) {
			t.Errorf("Serve: %v", err)
		}
	}()

	// First connection: the protocol panics while handling it.
	first, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("dial first: %v", err)
	}
	first.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, _ = first.Read(make([]byte, 1)) // wait for the handler to finish
	first.Close()

	// Second connection: the server is still up and serving.
	second, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatalf("the server died with the first connection: %v", err)
	}
	defer second.Close()
	second.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2)
	if _, err := second.Read(buf); err != nil {
		t.Fatalf("read after the panicking connection: %v", err)
	}
	if string(buf) != "ok" {
		t.Fatalf("got %q, want %q", buf, "ok")
	}
}
