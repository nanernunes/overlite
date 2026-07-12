package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestRewriteCasts(t *testing.T) {
	cases := map[string]string{
		"SELECT $1::int":         "SELECT CAST($1 AS int)",
		"SELECT $1::int + 1":     "SELECT CAST($1 AS int) + 1",
		"SELECT col::text":       "SELECT CAST(col AS text)",
		"SELECT 'x'::varchar":    "SELECT CAST('x' AS varchar)",
		"SELECT $1::int::text":   "SELECT CAST(CAST($1 AS int) AS text)",
		"SELECT (a + b)::float8": "SELECT CAST((a + b) AS float8)",
		"SELECT 1":               "SELECT 1", // untouched
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteCasts(in), "rewriteCasts(%q)", in)
	}
}

func TestRewriteInformationSchema(t *testing.T) {
	assert.Equal(t,
		`SELECT * FROM "information_schema.tables"`,
		rewriteInformationSchema("SELECT * FROM information_schema.tables"))
	assert.Equal(t,
		`SELECT * FROM "information_schema.columns" WHERE table_name = 'x'`,
		rewriteInformationSchema("SELECT * FROM information_schema.columns WHERE table_name = 'x'"))
	// Case-insensitive on the schema/table, other SQL untouched.
	assert.Equal(t,
		`SELECT * FROM "information_schema.tables"`,
		rewriteInformationSchema("SELECT * FROM INFORMATION_SCHEMA.TABLES"))
}

func TestRewritePgCatalogPrefix(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM pg_catalog.pg_class":       "SELECT * FROM pg_class",
		"SELECT pg_catalog.version()":             "SELECT version()",
		"JOIN pg_catalog.pg_namespace n ON n.oid": "JOIN pg_namespace n ON n.oid",
		"SELECT 1":                   "SELECT 1",
		"SELECT PG_CATALOG.PG_CLASS": "SELECT PG_CLASS", // case-insensitive
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewritePgCatalogPrefix(in), "rewritePgCatalogPrefix(%q)", in)
	}
}

func TestRewriteJSONFuncs(t *testing.T) {
	cases := map[string]string{
		"SELECT jsonb_build_object('a', 1)": "SELECT json_object('a', 1)",
		"SELECT json_build_array(1, 2)":     "SELECT json_array(1, 2)",
		"SELECT jsonb_agg(x)":               "SELECT json_group_array(x)",
		"SELECT jsonb_object_agg(k, v)":     "SELECT json_group_object(k, v)",
		"SELECT jsonb_typeof(doc)":          "SELECT json_type(doc)",
		"SELECT jsonb_array_length(doc)":    "SELECT json_array_length(doc)",
		"SELECT to_jsonb(x)":                "SELECT json_quote(x)",
		"SELECT json_extract(doc, '$.a')":   "SELECT json_extract(doc, '$.a')", // native, untouched
		"SELECT doc -> 'k', doc ->> 'k'":    "SELECT doc -> 'k', doc ->> 'k'",  // native operators
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteJSONFuncs(in), "rewriteJSONFuncs(%q)", in)
	}
}

func TestRewriteJSONPath(t *testing.T) {
	assert.Equal(t,
		`SELECT json_extract(doc, '$.a.b') FROM t`,
		rewriteJSONPath(`SELECT doc #> '{a,b}' FROM t`))
	assert.Equal(t,
		`WHERE json_extract(doc, '$.tags[0]') = 'x'`,
		rewriteJSONPath(`WHERE doc #>> '{tags,0}' = 'x'`))
	assert.Equal(t, "SELECT a FROM t", rewriteJSONPath("SELECT a FROM t")) // untouched
}

func TestRewriteAnyArray(t *testing.T) {
	// Full pipeline: OPERATOR(=) + ANY(ARRAY[...]) → IN (...).
	assert.Equal(t,
		"WHERE relkind IN ('r', 'S', 'v')",
		rewrite("WHERE relkind OPERATOR(pg_catalog.=) ANY (array['r', 'S', 'v'])"))
	assert.Equal(t,
		"WHERE x NOT IN (1, 2)",
		rewrite("WHERE x <> ALL (ARRAY[1, 2])"))
}

func TestRewriteNow(t *testing.T) {
	assert.Equal(t, "SELECT datetime('now')", rewriteNow("SELECT now()"))
	assert.Equal(t, "SELECT datetime('now')", rewriteNow("SELECT NOW( )"))
	assert.Equal(t, "SELECT datetime('now')", rewriteNow("SELECT transaction_timestamp()"))
	assert.Equal(t, "SELECT known", rewriteNow("SELECT known")) // untouched
}

func TestRewriteExtract(t *testing.T) {
	cases := map[string]string{
		"SELECT extract(year FROM d)":         "SELECT date_part('year', d)",
		"SELECT extract(MONTH from ts)":       "SELECT date_part('month', ts)",
		"WHERE extract(dow FROM created) = 0": "WHERE date_part('dow', created) = 0",
		"SELECT extract(day FROM now())":      "SELECT date_part('day', now())",
		"SELECT a, b":                         "SELECT a, b", // untouched
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteExtract(in), "rewriteExtract(%q)", in)
	}
}

func TestRewriteSerial(t *testing.T) {
	cases := map[string]string{
		"CREATE TABLE t (id SERIAL PRIMARY KEY, n TEXT)": "CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT, n TEXT)",
		"CREATE TABLE t (id BIGSERIAL PRIMARY KEY)":      "CREATE TABLE t (id INTEGER PRIMARY KEY AUTOINCREMENT)",
		"CREATE TABLE t (a SERIAL, b INT)":               "CREATE TABLE t (a INTEGER, b INT)",
		"ALTER TABLE t ADD COLUMN c SMALLSERIAL":         "ALTER TABLE t ADD COLUMN c INTEGER",
		"SELECT serial FROM t":                           "SELECT serial FROM t", // not DDL → untouched
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteSerial(in), "rewriteSerial(%q)", in)
	}
}

func TestRewriteNiladicFuncs(t *testing.T) {
	cases := map[string]string{
		"SELECT current_user":                  "SELECT current_user()",
		"SELECT current_schema, current_user":  "SELECT current_schema(), current_user()",
		"SELECT current_user()":                "SELECT current_user()",      // already a call
		`SELECT x AS "current_user"`:           `SELECT x AS "current_user"`, // quoted identifier
		"SELECT n.current_user":                "SELECT n.current_user",      // qualified
		"SELECT 'current_user'":                "SELECT 'current_user'",      // string literal
		"WHERE rolname = current_user AND a=1": "WHERE rolname = current_user() AND a=1",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteNiladicFuncs(in), "rewriteNiladicFuncs(%q)", in)
	}
}

func TestRewriteEscapeStrings(t *testing.T) {
	assert.Equal(t, `array_to_string(x, '\n')`, rewriteEscapeStrings(`array_to_string(x, E'\n')`))
	assert.Equal(t, `SELECT '\t', a`, rewriteEscapeStrings(`SELECT E'\t', a`))
	assert.Equal(t, "SELECT 'plain'", rewriteEscapeStrings("SELECT 'plain'")) // untouched
	// A lone e inside a string literal is content, not an E'' prefix.
	assert.Equal(t, "SELECT nextval('e')", rewriteEscapeStrings("SELECT nextval('e')"))
	assert.Equal(t, "SELECT ' escape '", rewriteEscapeStrings("SELECT ' escape '"))
	// e prefixing a real string still gets dropped even mid-identifier boundary.
	assert.Equal(t, `WHERE x = 'y'`, rewriteEscapeStrings(`WHERE x = E'y'`))
}

// TestRewriteStringLiteralAware verifies rewrite rules never touch text that
// lives inside a string literal.
func TestRewriteStringLiteralAware(t *testing.T) {
	// A quoted sequence name ending in a lone e survives the full pipeline.
	assert.Equal(t, "SELECT nextval('e')", rewrite("SELECT nextval('e')"))
	// Keywords/qualifiers inside a string literal are left alone.
	assert.Equal(t, "SELECT 'public.users'", rewrite("SELECT 'public.users'"))
	assert.Equal(t, "SELECT 'now()'", rewrite("SELECT 'now()'"))
	assert.Equal(t, "SELECT 'pg_catalog.x'", rewrite("SELECT 'pg_catalog.x'"))
	assert.Equal(t, "INSERT INTO t VALUES ('a serial number')",
		rewrite("INSERT INTO t VALUES ('a serial number')"))
}

func TestRewritePublicPrefix(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM public.clientes":      "SELECT * FROM clientes",
		`SELECT * FROM "public"."clientes"`:  `SELECT * FROM "clientes"`,
		"SELECT * FROM PUBLIC.clientes":      "SELECT * FROM clientes",
		"SELECT * FROM t WHERE x = 'public'": "SELECT * FROM t WHERE x = 'public'", // string untouched
		"SELECT * FROM mypublic.t":           "SELECT * FROM mypublic.t",           // not a word match
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewritePublicPrefix(in), "rewritePublicPrefix(%q)", in)
	}
}

func TestRewriteMatchOperators(t *testing.T) {
	cases := map[string]string{
		"a ~ 'x'":                "a REGEXP 'x'",
		"a !~ 'x'":               "NOT (a REGEXP 'x')",
		"nspname !~ '^pg_toast'": "NOT (nspname REGEXP '^pg_toast')",
		"n.nspname ~ 'pub'":      "n.nspname REGEXP 'pub'",
		"a ~* 'x'":               "a REGEXP '(?i)x'",
		"a !~* 'x'":              "NOT (a REGEXP '(?i)x')",
		"WHERE a = 1":            "WHERE a = 1", // untouched
		"a !~ 'p' AND b ~ 'q'":   "NOT (a REGEXP 'p') AND b REGEXP 'q'",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteMatchOperators(in), "rewriteMatchOperators(%q)", in)
	}
}

func TestRewriteOperatorSyntaxAndCollate(t *testing.T) {
	// psql's \dt <pattern> emits OPERATOR(pg_catalog.~) and COLLATE clauses;
	// after the full rewrite they must become a plain REGEXP.
	got := rewrite(`c.relname OPERATOR(pg_catalog.~) '^(clientes)$' COLLATE pg_catalog.default`)
	assert.Equal(t, `c.relname REGEXP '^(clientes)$'`, got)

	assert.Equal(t, "a REGEXP 'x'", rewrite("a OPERATOR(pg_catalog.~) 'x'"))
	assert.Equal(t, "SELECT a FROM t", rewriteCollate(`SELECT a COLLATE "default" FROM t`))
}

func TestRewriteSchemaQualifiedCast(t *testing.T) {
	// psql's \d uses casts whose type is schema-qualified; after stripping the
	// pg_catalog. prefix they become valid CASTs. A reg* cast on a column
	// resolves the oid to its catalog name.
	assert.Equal(t,
		"CAST((SELECT typname FROM pg_type WHERE oid = c.reloftype) AS text)",
		rewrite("c.reloftype::pg_catalog.regtype::pg_catalog.text"))
}

func TestRewriteRegClassCasts(t *testing.T) {
	// A numeric literal is a comparison seed → becomes a bare integer oid.
	assert.Equal(t, "1", rewrite("'1'::pg_catalog.regclass"))
	// A column reference is for display → resolves to the name.
	assert.Equal(t,
		"(SELECT relname FROM pg_class WHERE oid = conrelid)",
		rewrite("conrelid::pg_catalog.regclass"))
	// A quoted name literal is resolved the other way, name → oid, so it
	// compares to oid columns (obj_description('t'::regclass, ...), \d lookups).
	assert.Equal(t,
		"(SELECT oid FROM pg_class WHERE relname = 'emp')",
		rewrite("'emp'::regclass"))
	// A schema qualifier is dropped from the name.
	assert.Equal(t,
		"(SELECT oid FROM pg_class WHERE relname = 'emp')",
		rewrite("'public.emp'::regclass"))
}

func TestRewriteObjectDefs(t *testing.T) {
	assert.Contains(t,
		rewrite("pg_catalog.pg_get_indexdef(i.indexrelid, 0, true)"),
		"USING btree")
	assert.Contains(t,
		rewrite("pg_catalog.pg_get_constraintdef(con.oid, true)"),
		"FOREIGN KEY")
}

func TestRewriteArrayAndAny(t *testing.T) {
	assert.Equal(t,
		"(select rolname from pg_roles)",
		rewriteArraySubquery("array(select rolname from pg_roles)"))
	assert.Equal(t,
		"SELECT array_to_string((SELECT a FROM t), ',')",
		rewriteArraySubquery("SELECT array_to_string(ARRAY(SELECT a FROM t), ',')"))

	assert.Equal(t,
		"WHERE oid IN (pol.polroles)",
		rewriteAnyOperator("WHERE oid = any (pol.polroles)"))
	assert.Equal(t,
		"WHERE oid IN (pol.polroles)",
		rewriteAnyOperator("WHERE oid=ANY(pol.polroles)"))
}

func TestRewriteTrimFrom(t *testing.T) {
	cases := map[string]string{
		"trim(trailing ';' from x)":                     "rtrim(x, ';')",
		"trim(leading ' ' from name)":                   "ltrim(name, ' ')",
		"trim(both '-' from a)":                         "trim(a, '-')",
		"trim(trailing ';' from pg_get_ruledef(r.oid))": "rtrim(pg_get_ruledef(r.oid), ';')",
		"trim(x)":                                "trim(x)", // ordinary trim untouched
		"SELECT trim(both '.' from a), b FROM t": "SELECT trim(a, '.'), b FROM t",
	}
	for in, want := range cases {
		assert.Equalf(t, want, rewriteTrimFrom(in), "rewriteTrimFrom(%q)", in)
	}
}

func TestCountParams(t *testing.T) {
	cases := map[string]int{
		"SELECT 1":                    0,
		"SELECT $1":                   1,
		"SELECT $1, $2":               2,
		"SELECT $2, $1":               2, // highest index wins
		"INSERT INTO t VALUES (?, ?)": 2,
		"SELECT $1 WHERE a = $1":      1, // reused index counts once
	}
	for sql, want := range cases {
		assert.Equalf(t, want, countParams(sql), "countParams(%q)", sql)
	}
}
