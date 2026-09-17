package engine

import (
	"sync"
)

// A process serves many databases at once — one engine per file — so anything
// the catalog or the statement rewrites read about "the database" has to be
// per database, not per process. It is keyed by the path of the file, which is
// what the driver's connection hook is handed.

type dbState struct {
	name string // what current_database() answers
	path string

	// oidBand keeps one database's generated oids (enum types) from colliding
	// with another's, since the registry format_type() reads is shared by the
	// whole process.
	oidBand int64

	mu      sync.RWMutex
	schemas []string
}

var (
	dbStatesMu  sync.Mutex
	dbStates    = map[string]*dbState{}
	nextOIDBand int64
)

// stateFor returns the state for a database file, creating it on first use.
func stateFor(path string) *dbState {
	dbStatesMu.Lock()
	defer dbStatesMu.Unlock()
	if st, ok := dbStates[path]; ok {
		return st
	}
	nextOIDBand++
	st := &dbState{
		name:    dbNameFromPath(path),
		path:    path,
		oidBand: nextOIDBand * enumOIDBandWidth,
	}
	dbStates[path] = st
	return st
}

// forgetState drops a database's state, so a file that is deleted and recreated
// does not inherit the old one.
func forgetState(path string) {
	dbStatesMu.Lock()
	delete(dbStates, path)
	dbStatesMu.Unlock()
}

func (s *dbState) setSchemas(names []string) {
	s.mu.Lock()
	s.schemas = append(s.schemas[:0:0], names...)
	s.mu.Unlock()
}

func (s *dbState) schemaList() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.schemas
}

// dbName is the name current_database() answers for this database.
func (s *dbState) dbName() string {
	if s == nil {
		return "main"
	}
	return s.name
}
