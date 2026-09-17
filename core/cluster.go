package core

import "context"

// A Cluster is a set of databases served by one process, the way a PostgreSQL
// server hosts several. A client names the one it wants in its startup message
// and stays on it for the life of the connection; nothing in SQL crosses from
// one to another.
type Cluster interface {
	// Engine returns the engine backing a database, or an error naming what is
	// available when there is no such database.
	Engine(ctx context.Context, database string) (Engine, error)

	// Release says a caller is done with an engine it took from Engine, so a
	// cluster that caps how many it keeps open can retire this one.
	Release(database string)

	// Databases lists the names this cluster serves, sorted.
	Databases() ([]string, error)

	// CreateDatabase adds one. It fails when the name is already taken.
	CreateDatabase(ctx context.Context, database string) error

	// DropDatabase removes one and its file. It fails while clients are on it.
	DropDatabase(ctx context.Context, database string) error

	// Close releases every open engine.
	Close() error
}
