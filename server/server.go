// Package server accepts TCP connections and hands each one to a Protocol,
// backed by a single shared Engine.
package server

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"

	"overlite/core"
	"overlite/protocol"
)

// Server binds one Protocol to one Engine on a listening address. A single
// Server process owns the SQLite file; every client multiplexes through it.
type Server struct {
	proto  protocol.Protocol
	engine core.Engine
	ln     net.Listener

	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// New starts listening on addr (e.g. "127.0.0.1:5432", or ":0" for a random
// free port, useful in tests). Call Serve to accept connections.
func New(addr string, proto protocol.Protocol, engine core.Engine) (*Server, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	return &Server{
		proto:  proto,
		engine: engine,
		ln:     ln,
		conns:  make(map[net.Conn]struct{}),
	}, nil
}

// Addr returns the actual listening address (resolved port).
func (s *Server) Addr() string { return s.ln.Addr().String() }

// Serve accepts connections until ctx is cancelled or the listener is closed.
func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.ln.Close()
	}()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.track(conn)
		go s.handle(ctx, conn)
	}
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	defer s.untrack(conn)
	defer conn.Close()
	if err := s.proto.Serve(ctx, conn, s.engine); err != nil {
		log.Printf("%s: connection %s: %v", s.proto.Name(), conn.RemoteAddr(), err)
	}
}

// Close stops accepting and drops open connections.
func (s *Server) Close() error {
	err := s.ln.Close()
	s.mu.Lock()
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()
	return err
}

func (s *Server) track(c net.Conn) {
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
}

func (s *Server) untrack(c net.Conn) {
	s.mu.Lock()
	delete(s.conns, c)
	s.mu.Unlock()
}
