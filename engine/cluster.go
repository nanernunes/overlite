package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"overlite/core"
)

// reDatabaseName is what a database may be called. The name becomes a file
// name, so it is held to the same shape as a schema's.
var reDatabaseName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// ValidDatabaseName reports whether name can be served as a database.
func ValidDatabaseName(name string) bool {
	return reDatabaseName.MatchString(name) && len(name) <= 63
}

// --- one database -------------------------------------------------------------

// single serves exactly one database, the file overlite was pointed at.
type single struct {
	name string
	eng  *SQLite
}

// Single wraps one engine as a cluster of one.
func Single(name string, eng *SQLite) core.Cluster { return &single{name: name, eng: eng} }

func (s *single) Engine(_ context.Context, database string) (core.Engine, error) {
	// An empty name means the client did not ask for one.
	if database == "" || strings.EqualFold(database, s.name) {
		return s.eng, nil
	}
	return nil, &NoSuchDatabaseError{Name: database, Available: []string{s.name}}
}

func (s *single) Release(string)                               {}
func (s *single) Databases() ([]string, error)                 { return []string{s.name}, nil }
func (s *single) CreateDatabase(context.Context, string) error { return errNotADirectory }
func (s *single) DropDatabase(context.Context, string) error   { return errNotADirectory }
func (s *single) Close() error                                 { return s.eng.Close() }

var errNotADirectory = fmt.Errorf(
	"this server holds a single database; start it with --db-dir to create and drop databases")

// --- a directory of databases -------------------------------------------------

// Dir serves every <name>.db in a directory, opening each on demand. Idle
// engines are closed once maxOpen is exceeded, so a deployment can hold far
// more databases than it keeps open at once.
type Dir struct {
	dir     string
	maxOpen int

	mu   sync.Mutex
	open map[string]*openDB
	seq  uint64 // bumps on use, to find the least recently used
}

type openDB struct {
	eng  *SQLite
	refs int    // connections currently on it
	used uint64 // seq at last use
}

// DefaultMaxOpenDatabases is how many engines a Dir keeps open. Each one costs
// a SQLite connection pool and its catalog, so this trades memory for the
// latency of reopening a database that went cold.
const DefaultMaxOpenDatabases = 64

// OpenDir serves the databases in dir. It is created if it does not exist.
func OpenDir(dir string, maxOpen int) (*Dir, error) {
	if maxOpen <= 0 {
		maxOpen = DefaultMaxOpenDatabases
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create database directory %s: %w", dir, err)
	}
	return &Dir{dir: dir, maxOpen: maxOpen, open: map[string]*openDB{}}, nil
}

func (d *Dir) path(name string) string { return filepath.Join(d.dir, name+".db") }

// Databases lists the files in the directory.
func (d *Dir) Databases() ([]string, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("read database directory %s: %w", d.dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".db")
		if ValidDatabaseName(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (d *Dir) exists(name string) bool {
	_, err := os.Stat(d.path(name))
	return err == nil
}

// Engine opens a database, or returns it if it is already open. The caller
// must Release it when done.
func (d *Dir) Engine(_ context.Context, database string) (core.Engine, error) {
	if database == "" {
		return nil, &NoSuchDatabaseError{Name: database, Available: d.availableForError()}
	}
	if !ValidDatabaseName(database) || !d.exists(database) {
		return nil, &NoSuchDatabaseError{Name: database, Available: d.availableForError()}
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.open[database]; ok {
		e.refs++
		d.seq++
		e.used = d.seq
		return e.eng, nil
	}

	eng, err := Open(d.path(database))
	if err != nil {
		return nil, err
	}
	d.seq++
	d.open[database] = &openDB{eng: eng, refs: 1, used: d.seq}
	d.evictLocked()
	return eng, nil
}

// Release drops a caller's hold, making the database eligible to be closed.
func (d *Dir) Release(database string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.open[database]; ok && e.refs > 0 {
		e.refs--
	}
	d.evictLocked()
}

// evictLocked closes idle engines until at most maxOpen are open. An engine
// with a connection on it is never closed.
func (d *Dir) evictLocked() {
	for len(d.open) > d.maxOpen {
		var victim string
		var oldest uint64
		for name, e := range d.open {
			if e.refs > 0 {
				continue
			}
			if victim == "" || e.used < oldest {
				victim, oldest = name, e.used
			}
		}
		if victim == "" {
			return // everything open is in use
		}
		d.open[victim].eng.Close()
		delete(d.open, victim)
	}
}

// CreateDatabase creates the file and its catalog.
func (d *Dir) CreateDatabase(ctx context.Context, database string) error {
	if !ValidDatabaseName(database) {
		return fmt.Errorf("invalid database name %q: a database name must start with a letter "+
			"or underscore and contain only letters, digits, underscores and dashes, "+
			"because it becomes the name of the file that stores it", database)
	}
	if d.exists(database) {
		return fmt.Errorf("database %q already exists", database)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	eng, err := Open(d.path(database))
	if err != nil {
		return fmt.Errorf("create database %q: %w", database, err)
	}
	d.seq++
	d.open[database] = &openDB{eng: eng, used: d.seq}
	d.evictLocked()
	return nil
}

// DropDatabase closes the database and deletes its files.
func (d *Dir) DropDatabase(_ context.Context, database string) error {
	if !d.exists(database) {
		return fmt.Errorf("database %q does not exist", database)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if e, ok := d.open[database]; ok {
		if e.refs > 0 {
			return fmt.Errorf("database %q is being accessed by other users", database)
		}
		e.eng.Close()
		delete(d.open, database)
	}
	path := d.path(database)
	forgetState(path)
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("drop database %q: %w", database, err)
		}
	}
	return nil
}

func (d *Dir) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for name, e := range d.open {
		e.eng.Close()
		delete(d.open, name)
	}
	return nil
}

// availableForError lists a few names to put in a "no such database" message.
func (d *Dir) availableForError() []string {
	names, err := d.Databases()
	if err != nil {
		return nil
	}
	const max = 5
	if len(names) > max {
		return append(names[:max:max], "…")
	}
	return names
}

// NoSuchDatabaseError is returned when a client asks for a database this
// server does not serve. It carries what is available, because the usual cause
// is pointing at the wrong server or misspelling the name.
type NoSuchDatabaseError struct {
	Name      string
	Available []string
}

func (e *NoSuchDatabaseError) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("database %q does not exist", e.Name)
	}
	return fmt.Sprintf("database %q does not exist; this server serves %s",
		e.Name, strings.Join(e.Available, ", "))
}
