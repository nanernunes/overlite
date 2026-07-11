package postgres

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
)

// isCanceled reports whether err is the result of a cancelled query context
// (either the Go context error or SQLite's "interrupted").
func isCanceled(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "interrupt")
}

// sendExecError reports an execution error, using SQLSTATE 57014 for a cancelled
// query (simple-query path).
func (s *session) sendExecError(err error) error {
	if isCanceled(err) {
		return s.c.sendError("57014", "canceling statement due to user request")
	}
	return s.c.sendError("42000", err.Error())
}

// protoExecError is sendExecError for the extended-query path (arms the
// skip-until-Sync flag).
func (s *session) protoExecError(err error) error {
	if isCanceled(err) {
		return s.protoError("57014", "canceling statement due to user request")
	}
	return s.protoError("42000", err.Error())
}

// Query cancellation follows the Postgres model: each connection is assigned a
// random (pid, secret) sent in BackendKeyData; a client cancels a running query
// by opening a second connection and sending a CancelRequest carrying that pair.
// We map the pair to a canceler whose cancel func interrupts the connection's
// in-flight query context (SQLite honors context cancellation).

// canceler holds the cancel func for a connection's currently-running query, or
// nil when the connection is idle.
type canceler struct {
	mu sync.Mutex
	fn context.CancelFunc
}

func (cl *canceler) arm(fn context.CancelFunc) {
	cl.mu.Lock()
	cl.fn = fn
	cl.mu.Unlock()
}

func (cl *canceler) disarm() {
	cl.mu.Lock()
	cl.fn = nil
	cl.mu.Unlock()
}

func (cl *canceler) cancel() {
	cl.mu.Lock()
	fn := cl.fn
	cl.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// backends maps a (pid, secret) key to the connection's *canceler.
var backends sync.Map

func backendKey(pid, secret int32) uint64 {
	return uint64(uint32(pid))<<32 | uint64(uint32(secret))
}

func registerBackend(pid, secret int32) *canceler {
	cl := &canceler{}
	backends.Store(backendKey(pid, secret), cl)
	return cl
}

func unregisterBackend(pid, secret int32) {
	backends.Delete(backendKey(pid, secret))
}

// cancelBackend interrupts the running query of the connection identified by
// (pid, secret), if any. Unknown pairs are ignored (as Postgres does).
func cancelBackend(pid, secret int32) {
	if v, ok := backends.Load(backendKey(pid, secret)); ok {
		v.(*canceler).cancel()
	}
}

// cancelRequest carries the target backend of a CancelRequest startup packet.
type cancelRequest struct {
	pid    int32
	secret int32
}

// randInt32 returns a random non-zero int32 for a backend pid/secret.
func randInt32() int32 {
	var b [4]byte
	_, _ = rand.Read(b[:])
	v := int32(binary.BigEndian.Uint32(b[:]))
	if v == 0 {
		v = 1
	}
	return v
}
