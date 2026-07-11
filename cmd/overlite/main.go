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

	"overlite/engine"
	"overlite/protocol"
	"overlite/protocol/postgres"
	"overlite/server"
)

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
	)
	cmd := &cobra.Command{
		Use:   "overlite [db-file]",
		Short: "A PostgreSQL-speaking server backed by SQLite",
		Long: "overlite speaks the PostgreSQL wire protocol on the front and " +
			"stores everything in a single SQLite file on the back.\n\n" +
			"The database file may be given positionally or with --db; both work:\n" +
			"  overlite shop.db\n  overlite --db shop.db",
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 1 {
				db = args[0] // positional wins over --db
			}
			return run(driver, host, db)
		},
	}
	cmd.Flags().StringVar(&driver, "driver", envOr("OVERLITE_DRIVER", "postgres"),
		"wire protocol to speak (OVERLITE_DRIVER)")
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "listen address")
	cmd.Flags().StringVar(&db, "db", "postgres.db",
		"path to the SQLite file (or :memory:); its name becomes the database name")
	return cmd
}

func run(driver, host, db string) error {
	proto, err := selectDriver(driver)
	if err != nil {
		return err
	}

	eng, err := engine.Open(db)
	if err != nil {
		return fmt.Errorf("open engine: %w", err)
	}
	defer eng.Close()

	// The port is the driver's default, overridable via <DRIVER>_PORT
	// (e.g. POSTGRES_PORT).
	port := envInt(strings.ToUpper(driver)+"_PORT", proto.DefaultPort())
	srv, err := server.New(net.JoinHostPort(host, strconv.Itoa(port)), proto, eng)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.Printf("overlite: %s protocol on %s -> sqlite %s (db=%s user=%s auth=%s tls=%s)",
		driver, srv.Addr(), db, dbName(db), currentUser(), authMode(), tlsMode())
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
	if os.Getenv("POSTGRES_PASSWORD") == "" {
		return "trust"
	}
	switch strings.ToLower(os.Getenv("POSTGRES_HOST_AUTH_METHOD")) {
	case "trust":
		return "trust"
	case "password":
		return "password (cleartext)"
	default:
		return "md5"
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
