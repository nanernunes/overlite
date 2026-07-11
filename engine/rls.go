package engine

// Row-level security is emulated by overlite: SQLite has no RLS, so the flags
// and policies are recorded here and the protocol layer injects each policy's
// USING/WITH CHECK expression into statements that touch the table. Both tables
// are hidden from the catalog by the `_overlite_*` GLOB filter.

// rlsTableDDL records the per-table RLS flags (ENABLE / FORCE ROW LEVEL SECURITY).
const rlsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_rls (
  tablename TEXT PRIMARY KEY COLLATE NOCASE,
  enabled   INTEGER DEFAULT 0,
  forced    INTEGER DEFAULT 0
)`

// policiesTableDDL holds CREATE POLICY definitions. command is
// ALL/SELECT/INSERT/UPDATE/DELETE; roles is a comma-separated lower-cased list
// (” = to public); permissive is 1 for PERMISSIVE, 0 for RESTRICTIVE.
const policiesTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_policies (
  polname    TEXT COLLATE NOCASE,
  tablename  TEXT COLLATE NOCASE,
  command    TEXT,
  roles      TEXT,
  permissive INTEGER DEFAULT 1,
  using_expr TEXT,
  check_expr TEXT
)`
