package postgres

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSplitStatements(t *testing.T) {
	cases := map[string][]string{
		"SELECT 1":            {"SELECT 1"},
		"SELECT 1; SELECT 2;": {"SELECT 1", "SELECT 2"},
		"-- a\nCREATE SCHEMA x;\n\n-- b\nCREATE TABLE t (a int);\n": {"-- a\nCREATE SCHEMA x", "-- b\nCREATE TABLE t (a int)"},
		"SELECT 'a;b'; SELECT 2":                                    {"SELECT 'a;b'", "SELECT 2"},
		"SELECT 'it''s;'; SELECT 2":                                 {"SELECT 'it''s;'", "SELECT 2"},
		`SELECT E'\';'; SELECT 2`:                                   {`SELECT E'\';'`, "SELECT 2"},
		`SELECT "a;b" FROM t; SELECT 2`:                             {`SELECT "a;b" FROM t`, "SELECT 2"},
		"SELECT 1 -- x;y\n; SELECT 2":                               {"SELECT 1 -- x;y", "SELECT 2"},
		"SELECT /* ; */ 1; SELECT 2":                                {"SELECT /* ; */ 1", "SELECT 2"},
		"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql; SELECT f()": {
			"CREATE FUNCTION f() RETURNS int AS $$ SELECT 1; $$ LANGUAGE sql", "SELECT f()"},
		"DO $body$ BEGIN; END $body$; SELECT 2": {"DO $body$ BEGIN; END $body$", "SELECT 2"},
		"SELECT $1; SELECT $2":                  {"SELECT $1", "SELECT $2"},
		"; -- only comments ;":                  nil,
		"CREATE TRIGGER t_ai AFTER INSERT ON t BEGIN INSERT INTO a VALUES (CASE WHEN 1 THEN 'x' END); UPDATE a SET m = 'y'; END; SELECT 2": {
			"CREATE TRIGGER t_ai AFTER INSERT ON t BEGIN INSERT INTO a VALUES (CASE WHEN 1 THEN 'x' END); UPDATE a SET m = 'y'; END", "SELECT 2"},
		"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END; SELECT f()": {
			"CREATE FUNCTION f() RETURNS int LANGUAGE sql BEGIN ATOMIC SELECT 1; END", "SELECT f()"},
		"BEGIN; SELECT 1; END": {"BEGIN", "SELECT 1", "END"},
	}
	for in, want := range cases {
		assert.Equalf(t, want, splitStatements(in), "splitStatements(%q)", in)
	}
}
