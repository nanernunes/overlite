package engine

import "testing"

// A RETURNING statement is introspected by rebuilding it as a read-only SELECT
// against the same table. The table pattern has to survive every quoting
// combination a client may send; capturing only the first quoted run turned
// `"sales"."orders"` into the table `"sales"`, and the query failed with
// `no such table: sales`.
func TestIntrospectionSQLTableNames(t *testing.T) {
	cases := map[string]string{
		`INSERT INTO t VALUES ($1) RETURNING id`:                          `SELECT id FROM t WHERE 0`,
		`INSERT INTO "t" VALUES ($1) RETURNING id`:                        `SELECT id FROM "t" WHERE 0`,
		`INSERT INTO sales.orders VALUES ($1) RETURNING id`:               `SELECT id FROM sales.orders WHERE 0`,
		`INSERT INTO "sales"."orders" VALUES ($1) RETURNING id`:           `SELECT id FROM "sales"."orders" WHERE 0`,
		`INSERT INTO sales."orders" VALUES ($1) RETURNING id`:             `SELECT id FROM sales."orders" WHERE 0`,
		`INSERT INTO "sales".orders VALUES ($1) RETURNING id`:             `SELECT id FROM "sales".orders WHERE 0`,
		`UPDATE "sales"."orders" SET n = n + 1 WHERE id = $1 RETURNING n`: `SELECT n FROM "sales"."orders" WHERE 0`,
		`DELETE FROM "sales"."orders" WHERE id = $1 RETURNING id, n`:      `SELECT id, n FROM "sales"."orders" WHERE 0`,
	}
	for in, want := range cases {
		got, ok := introspectionSQL(in)
		if !ok {
			t.Errorf("introspectionSQL(%q) reported no rows", in)
			continue
		}
		if got != want {
			t.Errorf("introspectionSQL(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestIntrospectionSQLNonReturning(t *testing.T) {
	for _, sql := range []string{
		`INSERT INTO t VALUES (1)`,
		`CREATE TABLE t (id int)`,
		`UPDATE t SET n = 1`,
	} {
		if _, ok := introspectionSQL(sql); ok {
			t.Errorf("introspectionSQL(%q) = ok, want no rows", sql)
		}
	}
}
