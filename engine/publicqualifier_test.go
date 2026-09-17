package engine

import "testing"

func TestStripPublicQualifier(t *testing.T) {
	cases := map[string]string{
		"SELECT * FROM public.clientes":      "SELECT * FROM clientes",
		`SELECT * FROM "public"."clientes"`:  `SELECT * FROM "clientes"`,
		`SELECT * FROM public."clientes"`:    `SELECT * FROM "clientes"`,
		`SELECT * FROM "public".clientes`:    "SELECT * FROM clientes",
		"SELECT * FROM PUBLIC.clientes":      "SELECT * FROM clientes",
		"SELECT * FROM t WHERE x = 'public'": "SELECT * FROM t WHERE x = 'public'", // string untouched
		"SELECT * FROM mypublic.t":           "SELECT * FROM mypublic.t",           // not a word match
		"SELECT * FROM t":                    "SELECT * FROM t",                    // nothing to do
		`SELECT "notpublic"."t"`:             `SELECT "notpublic"."t"`,
	}
	for in, want := range cases {
		if got := stripPublicQualifier(in); got != want {
			t.Errorf("stripPublicQualifier(%q) = %q, want %q", in, got, want)
		}
	}
}
