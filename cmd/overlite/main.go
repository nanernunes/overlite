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
		port   int
		maxDBs int
	)
	cmd := &cobra.Command{
		Use:   "overlite [db-file]",
		Short: "A PostgreSQL-speaking server backed by SQLite",
		Long: "overlite speaks the PostgreSQL wire protocol on the front and " +
			"stores everything in SQLite files on the back.\n\n" +
			"A SQLite file is a database, so the file you point at is the database " +
			"you connect to, and the directory holding it is the rest:\n" +
			"  overlite shop.db\n  overlite --db /data/postgres.db\n\n" +
			"CREATE DATABASE writes a new file beside it, and a client reaches one " +
			"by the database name it connects to.",
		Version:       version,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				db = args[0] // positional wins over --db
			}
			return run(driver, host, db, port, maxDBs)
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
	cmd.Flags().IntVar(&maxDBs, "max-open-databases", engine.DefaultMaxOpenDatabases,
		"how many databases to keep open at once; the rest reopen on demand")
	return cmd
}

func run(driver, host, db string, port, maxDBs int) error {
	proto, err := selectDriver(driver)
	if err != nil {
		return err
	}

	cluster, describe, err := openCluster(db, maxDBs)
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

// openCluster builds the set of databases to serve. A file on disk brings its
// directory with it: every <name>.db beside it is a database too. An in-memory
// database has no directory, so it is served on its own.
func openCluster(db string, maxDBs int) (core.Cluster, string, error) {
	if db == ":memory:" {
		eng, err := engine.Open(db)
		if err != nil {
			return nil, "", err
		}
		return engine.Single("main", eng), "sqlite :memory: (db=main)", nil
	}

	cluster, err := engine.OpenCluster(db, maxDBs)
	if err != nil {
		return nil, "", fmt.Errorf("open %s: %w", db, describeDBPath(db, err))
	}
	names, _ := cluster.Databases()
	return cluster, fmt.Sprintf("sqlite %s (db=%s, %d in %s, %d kept open)",
		db, dbName(db), len(names), filepath.Dir(db), maxDBs), nil
}
