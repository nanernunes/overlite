// Package protocol defines the extension point of overlite: a Protocol speaks
// some database's wire language on a raw connection and drives a core.Engine.
//
// This is the interface you implement once per database dialect: Postgres
// first, then MySQL, HTTP, and so on. Everything above (server, engine) stays
// the same.
package protocol

import (
	"context"
	"net"

	"overlite/core"
)

// Protocol handles the full lifetime of one client connection in a given wire
// language: handshake/auth, reading statements, executing them against the
// engine, and encoding results back.
type Protocol interface {
	// Name identifies the dialect, e.g. "postgres".
	Name() string

	// DefaultPort is the port this protocol conventionally listens on (e.g.
	// 5432 for postgres). Overridable via the <DRIVER>_PORT env var.
	DefaultPort() int

	// Serve owns conn until the client disconnects or an unrecoverable error
	// occurs. The caller closes conn afterwards. Implementations must not
	// assume anything about the engine beyond the core.Engine contract.
	Serve(ctx context.Context, conn net.Conn, engine core.Engine) error
}
