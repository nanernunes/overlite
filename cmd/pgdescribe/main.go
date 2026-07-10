// Command pgdescribe reproduces psql's \d for one table against overlite,
// rendering the output the way psql does. It's a manual harness for eyeballing
// catalog completeness (not part of the test suite).
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/jackc/pgx/v5"
)

func main() {
	table := "clientes"
	if len(os.Args) > 1 {
		table = os.Args[1]
	}
	ctx := context.Background()

	cfg, _ := pgx.ParseConfig("postgres://overlite@127.0.0.1:5432/main?sslmode=disable")
	cfg.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	conn, err := pgx.ConnectConfig(ctx, cfg)
	must(err)
	defer conn.Close(ctx)

	// Ensure the demo table exists.
	conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS clientes (id INTEGER PRIMARY KEY, nome TEXT NOT NULL)`)

	// 1) resolve oid
	var oid int64
	err = conn.QueryRow(ctx, fmt.Sprintf(
		`SELECT c.oid FROM pg_catalog.pg_class c
		 JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname OPERATOR(pg_catalog.~) '^(%s)$' AND pg_catalog.pg_table_is_visible(c.oid)`, table)).Scan(&oid)
	if err != nil {
		fmt.Printf("Did not find any relation named \"%s\": %v\n", table, err)
		return
	}

	fmt.Printf("%*sTable \"public.%s\"\n", 16, "", table)

	// 2) columns
	rows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT a.attname, pg_catalog.format_type(a.atttypid, a.atttypmod), a.attnotnull
		 FROM pg_catalog.pg_attribute a
		 WHERE a.attrelid = '%d' AND a.attnum > 0 AND NOT a.attisdropped
		 ORDER BY a.attnum`, oid))
	must(err)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 1, ' ', 0)
	fmt.Fprintln(tw, " Column\t| Type\t| Nullable")
	fmt.Fprintln(tw, "--------\t+------\t+---------")
	for rows.Next() {
		var name, typ string
		var notnull bool
		must(rows.Scan(&name, &typ, &notnull))
		null := ""
		if notnull {
			null = "not null"
		}
		fmt.Fprintf(tw, " %s\t| %s\t| %s\n", name, typ, null)
	}
	must(rows.Err())
	tw.Flush()

	// 3) indexes (explicit + primary key)
	var indexLines []string
	prows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT conname, ov_cols FROM pg_catalog.pg_constraint
		 WHERE conrelid = '%d' AND contype = 'p'`, oid))
	must(err)
	for prows.Next() {
		var name, cols string
		must(prows.Scan(&name, &cols))
		indexLines = append(indexLines, fmt.Sprintf("    \"%s\" PRIMARY KEY, btree (%s)", name, cols))
	}
	must(prows.Err())

	irows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT c2.relname, i.indisunique, i.ov_cols
		 FROM pg_catalog.pg_index i JOIN pg_catalog.pg_class c2 ON c2.oid = i.indexrelid
		 WHERE i.indrelid = '%d'`, oid))
	must(err)
	for irows.Next() {
		var name, cols string
		var uniq bool
		must(irows.Scan(&name, &uniq, &cols))
		kind := "btree"
		if uniq {
			kind = "UNIQUE, btree"
		}
		indexLines = append(indexLines, fmt.Sprintf("    \"%s\" %s (%s)", name, kind, cols))
	}
	must(irows.Err())
	if len(indexLines) > 0 {
		fmt.Println("Indexes:")
		fmt.Println(strings.Join(indexLines, "\n"))
	}

	// 4) foreign keys
	frows, err := conn.Query(ctx, fmt.Sprintf(
		`SELECT ov_cols, ov_ref FROM pg_catalog.pg_constraint
		 WHERE conrelid = '%d' AND contype = 'f'`, oid))
	must(err)
	var fks []string
	for frows.Next() {
		var cols, ref string
		must(frows.Scan(&cols, &ref))
		fks = append(fks, fmt.Sprintf("    FOREIGN KEY (%s) REFERENCES %s", cols, ref))
	}
	must(frows.Err())
	if len(fks) > 0 {
		fmt.Println("Foreign-key constraints:")
		fmt.Println(strings.Join(fks, "\n"))
	}
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}
