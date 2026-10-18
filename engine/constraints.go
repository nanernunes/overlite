package engine

import (
	"regexp"
	"strings"
)

// SQLite keeps a constraint's name in the table's DDL but reports neither it
// nor anything keyed by it: pragma_foreign_key_list numbers the foreign keys
// (last declared first) and the primary key has no name at all. Postgres
// clients read the names back from pg_constraint and compare them with the
// ones they created: a migration tool that sees "fk_Subject_0" where it wrote
// "Subject_schoolId_fkey" decides the constraint is gone and recreates it on
// every run. So the names are recovered from the DDL, falling back to the
// names Postgres itself gives an unnamed constraint.

// sqlIdent matches one identifier as SQLite accepts it: "double quoted",
// `backticked`, [bracketed] or bare.
const sqlIdent = `(?:"(?:[^"]|"")+"|` + "`[^`]+`" + `|\[[^\]]+\]|[A-Za-z_][\w$]*)`

var (
	reNamedFK = regexp.MustCompile(`(?is)\bCONSTRAINT\s+(` + sqlIdent + `)\s+FOREIGN\s+KEY\s*\(([^)]*)\)\s*REFERENCES\s+(` +
		sqlIdent + `(?:\s*\.\s*` + sqlIdent + `)?)`)
	reNamedPK   = regexp.MustCompile(`(?is)\bCONSTRAINT\s+(` + sqlIdent + `)\s+PRIMARY\s+KEY\b`)
	reQualified = regexp.MustCompile(`^(` + sqlIdent + `)\s*\.\s*(` + sqlIdent + `)$`)
)

// unquoteSQLIdent strips the quoting from one identifier ("a""b" -> a"b).
func unquoteSQLIdent(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		switch {
		case s[0] == '"' && s[len(s)-1] == '"':
			return strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
		case s[0] == '`' && s[len(s)-1] == '`', s[0] == '[' && s[len(s)-1] == ']':
			return s[1 : len(s)-1]
		}
	}
	return s
}

// identList splits a parenthesized column list body into unquoted names.
func identList(body string) []string {
	var out []string
	for _, p := range strings.Split(body, ",") {
		if p = unquoteSQLIdent(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// relationName drops a schema qualifier: public.users and "public"."users"
// both name users.
func relationName(ref string) string {
	ref = strings.TrimSpace(ref)
	if m := reQualified.FindStringSubmatch(ref); m != nil {
		return unquoteSQLIdent(m[2])
	}
	return unquoteSQLIdent(ref)
}

// fkConstraintName names the foreign key of table over fromCols (in key order)
// that references refTable: the name its DDL gave it, or Postgres' default
// <table>_<col>[_<col>...]_fkey.
func fkConstraintName(ddl, table string, fromCols []string, refTable string) string {
	for _, m := range reNamedFK.FindAllStringSubmatch(ddl, -1) {
		cols := identList(m[2])
		if strings.EqualFold(relationName(m[3]), refTable) && equalFoldList(cols, fromCols) {
			return unquoteSQLIdent(m[1])
		}
	}
	return table + "_" + strings.Join(fromCols, "_") + "_fkey"
}

// pkConstraintName is the primary key's name from the DDL, or Postgres' default
// <table>_pkey.
func pkConstraintName(ddl, table string) string {
	if m := reNamedPK.FindStringSubmatch(ddl); m != nil {
		return unquoteSQLIdent(m[1])
	}
	return table + "_pkey"
}

func equalFoldList(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(a[i], b[i]) {
			return false
		}
	}
	return true
}

// defaultOpclassOID is the oid of the btree operator class Postgres gives an
// index column of the declared type by default (text_ops for text and varchar,
// int4_ops for integer, ...), as pg_opclass lists it. Clients compare an index
// column's opclass with the default to decide whether it was asked for.
func defaultOpclassOID(decl string) int64 {
	switch sqliteTypeOID(decl) {
	case typeOIDBool:
		return 10003
	case typeOIDBytea:
		return 10006
	case typeOIDDate:
		return 3122
	case typeOIDFloat8:
		return 3123
	case typeOIDInt2:
		return 1979
	case typeOIDInt4:
		return 1978
	case typeOIDInt8:
		return 3124
	case typeOIDNumeric:
		return 3125
	case typeOIDTimestamptz:
		return 3127
	case typeOIDTimestamp:
		return 3128
	case typeOIDUUID:
		return 10065
	case typeOIDJSONB:
		return 10088
	default: // text, varchar, and json, which has no btree class of its own
		return 3126
	}
}
