package engine

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"sync"
)

// enumNameByOID lets the process-global format_type() scalar render an enum
// type's oid as its name (scalars have no DB access). It is refreshed from
// _overlite_enum_types on every connection setup, so a type created in a prior
// session (or by another connection) is picked up on connect. Keys are oids.
var enumNameByOID sync.Map

// refreshEnumNames stores oid:name pairs (as produced by the enum-name query in
// setupConnection) into the global registry.
func refreshEnumNames(pairs []string) {
	for _, p := range pairs {
		i := strings.IndexByte(p, ':')
		if i < 0 {
			continue
		}
		oid, err := strconv.ParseInt(p[:i], 10, 64)
		if err != nil {
			continue
		}
		enumNameByOID.Store(oid, p[i+1:])
	}
}

func lookupEnumName(oid int64) (string, bool) {
	if v, ok := enumNameByOID.Load(oid); ok {
		return v.(string), true
	}
	return "", false
}

// Enum types are stored in two internal tables in the main database:
// _overlite_enum_types (one row per type; its rowid seeds the pg_type oid) and
// _overlite_enums (one row per label, with a sort order). SQLite has no enum
// types, so the protocol layer records CREATE/ALTER/DROP TYPE ... AS ENUM here
// and rewrites an enum column to TEXT plus a CHECK constraint — native SQLite,
// so the file stays usable without overlite. Both tables are hidden from the
// catalog by the `_overlite_*` GLOB filter (catalog_views.go).
//
// enumOIDBase offsets a type's rowid into an oid range that won't collide with
// the built-in pg_type oids or the sequence/pg_class ranges.
const enumOIDBase = 90000000

const enumTypesTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_enum_types (
  typname TEXT PRIMARY KEY COLLATE NOCASE
)`

const enumLabelsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_enums (
  typname   TEXT COLLATE NOCASE,
  label     TEXT,
  sortorder REAL
)`

// Composite types (CREATE TYPE ... AS (...)) are recorded by name so they show
// in pg_type (typtype 'c') and \dT; their fields aren't modeled (SQLite has no
// composite storage).
const compositeTypesTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_composite_types (
  typname TEXT PRIMARY KEY COLLATE NOCASE
)`

// enumLabelsFromDDL pulls the label list out of the CHECK an enum column
// carries — `m text CHECK (m IN ('sad', 'ok', 'happy'))` yields "sad,ok,happy",
// in the enum's own order. The declared SQLite type is only ever "text", so the
// CHECK is what still says which enum the column was declared as; matching that
// list against _overlite_enums recovers the type. Returns "" when the column
// has no such CHECK.
//
// The list is joined with "," to be compared against group_concat's default,
// which means a label containing a comma simply fails to match and the column
// reports as text — the same answer as before, never a wrong type.
func enumLabelsFromDDL(ddl, col string) string {
	low, lowCol := strings.ToLower(ddl), strings.ToLower(col)
	for _, want := range []string{
		"check (" + lowCol + " in (",
		`check ("` + lowCol + `" in (`,
	} {
		i := strings.Index(low, want)
		if i < 0 {
			continue
		}
		rest := ddl[i+len(want):]
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			continue
		}
		return strings.Join(sqlStringLiterals(rest[:end]), ",")
	}
	return ""
}

// sqlStringLiterals returns the single-quoted values in s, in order, with ”
// unescaped back to a lone quote.
func sqlStringLiterals(s string) []string {
	var out []string
	for i := 0; i < len(s); i++ {
		if s[i] != '\'' {
			continue
		}
		var b strings.Builder
		i++
		for i < len(s) {
			if s[i] == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				break
			}
			b.WriteByte(s[i])
			i++
		}
		out = append(out, b.String())
	}
	return out
}

// enumTypesTable is the table whose writes invalidate the oid->name registry.
const enumTypesTable = "_overlite_enum_types"

// refreshEnumNamesFrom reloads the registry format_type() reads, from the
// connection that just changed it.
func refreshEnumNamesFrom(ctx context.Context, c *sql.Conn) {
	rows, err := c.QueryContext(ctx,
		"SELECT (rowid + "+strconv.Itoa(enumOIDBase)+") || ':' || typname FROM "+enumTypesTable)
	if err != nil {
		return
	}
	defer rows.Close()
	var pairs []string
	for rows.Next() {
		var v string
		if rows.Scan(&v) == nil {
			pairs = append(pairs, v)
		}
	}
	refreshEnumNames(pairs)
}
