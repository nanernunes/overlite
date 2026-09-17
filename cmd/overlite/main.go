// Command overlite runs a lightweight server that speaks a database wire
// protocol on the front and stores everything in a single SQLite file.
package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"overlite/core"
	"overlite/engine"
	"overlite/protocol"
	"overlite/protocol/postgres"
	"overlite/server"
)

// version is stamped at build time by `make build` (-X main.version); the
// release workflow passes the tag.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		log.Fatal(err)
	}
}

func newRootCmd() *cobra.Command {
	var (
		driver string
		host   string
		db     string
		dbDir  string
		port   int
		maxDBs int
	)
	cmd := &cobra.Command{
		Use:   "overlite [db-file]",
		Short: "A PostgreSQL-speaking server backed by SQLite",
		Long: "overlite speaks the PostgreSQL wire protocol on the front and " +
			"stores everything in SQLite files on the back.\n\n" +
			"One database, given positionally or with --db:\n" +
			"  overlite shop.db\n  overlite --db shop.db\n\n" +
			"Or a directory of them, one file per database, each opened on demand:\n" +
			"  overlite --db-dir /data\n" +
			"The client picks with the database name it connects to.",
		Version:       version,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				db = args[0] // positional wins over --db
			}
			return run(driver, host, db, dbDir, port, maxDBs)
		},
	}
	// Cobra's default is "overlite version v0.1.0"; the word adds nothing.
	cmd.SetVersionTemplate("{{.Name}} {{.Version}}\n")
	cmd.Flags().StringVar(&driver, "driver", envOr("OVERLITE_DRIVER", "postgres"),
		"wire protocol to speak (OVERLITE_DRIVER)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "listen address")
	// 0 means "whatever <DRIVER>_PORT or the protocol says", resolved in run
	// once the driver is known.
	cmd.Flags().IntVar(&port, "port", 0,
		"port to listen on (default: the protocol's, or <DRIVER>_PORT)")
	cmd.Flags().StringVar(&db, "db", "postgres.db",
		"path to the SQLite file (or :memory:); its name becomes the database name")
	cmd.Flags().StringVar(&dbDir, "db-dir", "",
		"serve a directory of databases: <name>.db per database, opened on demand")
	cmd.Flags().IntVar(&maxDBs, "max-open-databases", engine.DefaultMaxOpenDatabases,
		"with --db-dir, how many databases to keep open at once")
	return cmd
}

func run(driver, host, db, dbDir string, port, maxDBs int) error {
	proto, err := selectDriver(driver)
	if err != nil {
		return err
	}

	cluster, describe, err := openCluster(db, dbDir, maxDBs)
	if err != nil {
		return err
	}
	defer cluster.Close()

	// --port wins; otherwise the driver's default, overridable via
	// <DRIVER>_PORT (e.g. POSTGRES_PORT).
	if port == 0 {
		port = envInt(strings.ToUpper(driver)+"_PORT", proto.DefaultPort())
	}
	srv, err := server.New(net.JoinHostPort(host, strconv.Itoa(port)), proto, cluster)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("overlite: %s protocol on %s -> %s (user=%s auth=%s tls=%s)",
		driver, srv.Addr(), describe, currentUser(), authMode(), tlsMode())
	return srv.Serve(ctx)
}

// selectDriver resolves a driver name to its protocol. Postgres is the only one
// today; the seam is here for MySQL/HTTP/... later.
func selectDriver(name string) (protocol.Protocol, error) {
	switch name {
	case "postgres":
		return postgres.New(), nil
	default:
		return nil, fmt.Errorf("unknown driver %q (supported: postgres)", name)
	}
}

// dbName mirrors the engine's database-name derivation for the startup log.
func dbName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".db")
}

func currentUser() string {
	if u := os.Getenv("POSTGRES_USER"); u != "" {
		return u
	}
	return "postgres"
}

func authMode() string {
	dir := os.Getenv("OVERLITE_HBA_DIR")
	if dir == "" {
		dir = "."
	}
	for _, name := range []string{"pg_hba.conf", "pg_hba.yaml", "pg_hba.yml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			return "pg_hba (" + name + ")"
		}
	}
	if os.Getenv("POSTGRES_PASSWORD") == "" {
		return "trust"
	}
	switch strings.ToLower(os.Getenv("POSTGRES_HOST_AUTH_METHOD")) {
	case "trust":
		return "trust"
	case "password":
		return "password (cleartext)"
	case "md5":
		return "md5"
	default:
		return "scram-sha-256"
	}
}

func tlsMode() string {
	if (os.Getenv("POSTGRES_SSL_CERT") != "" && os.Getenv("POSTGRES_SSL_KEY") != "") ||
		os.Getenv("POSTGRES_SSL") != "" {
		return "on"
	}
	return "off"
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

// describeDBPath explains the common ways --db is wrong. SQLite reports only
// "unable to open database file", which says nothing about what to fix.
func describeDBPath(db string, err error) error {
	if db == "" || db == ":memory:" {
		return err
	}
	if info, statErr := os.Stat(db); statErr == nil && info.IsDir() {
		return fmt.Errorf("%s is a directory; --db takes the path to a SQLite file, "+
			"e.g. --db %s: %w", db, filepath.Join(db, "postgres.db"), err)
	}
	dir := filepath.Dir(db)
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return fmt.Errorf("the directory %s does not exist: %w", dir, err)
	}
	return err
}

// openCluster builds the set of databases to serve: one file, or a directory
// of them. It returns a line describing the choice for the startup log.
func openCluster(db, dbDir string, maxDBs int) (core.Cluster, string, error) {
	if dbDir != "" {
		dir, err := engine.OpenDir(dbDir, maxDBs)
		if err != nil {
			return nil, "", err
		}
		names, _ := dir.Databases()
		return dir, fmt.Sprintf("sqlite dir %s (%d databases, %d kept open)",
			dbDir, len(names), maxDBs), nil
	}

	eng, err := engine.Open(db)
	if err != nil {
		return nil, "", fmt.Errorf("open engine %s: %w", db, describeDBPath(db, err))
	}
	name := dbName(db)
	return engine.Single(name, eng), fmt.Sprintf("sqlite %s (db=%s)", db, name), nil
}
