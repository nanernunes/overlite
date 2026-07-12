package engine

// Sequences are stored in an internal table `_overlite_sequences` in the main
// database (persistent, shared across connections). SQLite has no sequences, so
// the protocol layer implements CREATE/ALTER/DROP SEQUENCE plus nextval/currval/
// setval/lastval against this table, expanding each call to a concrete integer
// before it reaches SQLite. That keeps the file a plain SQLite database (values
// stored are ordinary integers, never a nextval() reference), usable without
// overlite. The table is hidden from the catalog (the `_overlite_*` GLOB filter
// in catalog_views.go).
//
// last_value holds the current value; is_called distinguishes "not yet advanced"
// (next nextval returns start_value) from "advanced" (next returns last_value +
// increment). min_value/max_value are materialized at CREATE time so nextval's
// bounds/CYCLE logic needs no defaults.
// columnDefaultsTableDDL records `DEFAULT nextval('seq')` column defaults (which
// SQLite can't express), so the protocol can inject the value on insert.
const columnDefaultsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_defaults (
  tablename TEXT COLLATE NOCASE,
  colname   TEXT COLLATE NOCASE,
  seqname   TEXT
)`

const sequencesTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_sequences (
  seqname     TEXT PRIMARY KEY COLLATE NOCASE,
  last_value  INTEGER NOT NULL DEFAULT 1,
  increment   INTEGER NOT NULL DEFAULT 1,
  min_value   INTEGER NOT NULL DEFAULT 1,
  max_value   INTEGER NOT NULL DEFAULT 9223372036854775807,
  start_value INTEGER NOT NULL DEFAULT 1,
  cache_size  INTEGER NOT NULL DEFAULT 1,
  is_cycled   INTEGER NOT NULL DEFAULT 0,
  is_called   INTEGER NOT NULL DEFAULT 0
)`
