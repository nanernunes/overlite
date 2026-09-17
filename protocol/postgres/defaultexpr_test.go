package postgres

import "testing"

func TestRewriteDefaultExpr(t *testing.T) {
	cases := map[string]string{
		// A computed default has to be parenthesised for SQLite.
		`CREATE TABLE t (at timestamptz DEFAULT datetime('now'))`:  `CREATE TABLE t (at timestamptz DEFAULT (datetime('now')))`,
		`CREATE TABLE t (id uuid DEFAULT gen_random_uuid())`:       `CREATE TABLE t (id uuid DEFAULT (gen_random_uuid()))`,
		`ALTER TABLE t ADD COLUMN at text DEFAULT datetime('now')`: `ALTER TABLE t ADD COLUMN at text DEFAULT (datetime('now'))`,

		// Two of them in one statement.
		`CREATE TABLE t (a text DEFAULT lower('X'), b text DEFAULT upper('y'))`: `CREATE TABLE t (a text DEFAULT (lower('X')), b text DEFAULT (upper('y')))`,

		// Already parenthesised, literal, or a bare keyword: untouched.
		`CREATE TABLE t (at text DEFAULT (datetime('now')))`: `CREATE TABLE t (at text DEFAULT (datetime('now')))`,
		`CREATE TABLE t (at text DEFAULT CURRENT_TIMESTAMP)`: `CREATE TABLE t (at text DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TABLE t (n int DEFAULT 0)`:                   `CREATE TABLE t (n int DEFAULT 0)`,
		`CREATE TABLE t (s text DEFAULT 'x')`:                `CREATE TABLE t (s text DEFAULT 'x')`,
		`CREATE TABLE t (s text DEFAULT NULL)`:               `CREATE TABLE t (s text DEFAULT NULL)`,

		// A string that merely contains the word is left alone.
		`INSERT INTO t VALUES ('DEFAULT now()')`: `INSERT INTO t VALUES ('DEFAULT now()')`,
	}
	for in, want := range cases {
		if got := rewriteDefaultExpr(in); got != want {
			t.Errorf("rewriteDefaultExpr(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}
