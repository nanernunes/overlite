package engine

import (
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
