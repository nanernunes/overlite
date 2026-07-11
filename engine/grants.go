package engine

// Privilege enforcement is emulated by overlite: SQLite has no per-object
// privileges, so GRANT/REVOKE are recorded in `_overlite_grants` and table
// ownership in `_overlite_owners`. The protocol layer consults them before it
// runs a statement (see protocol/postgres/privileges.go). Both tables live in
// the main database, are persistent, and are hidden from the catalog by the
// `_overlite_*` GLOB filter.

// grantsTableDDL holds one row per (grantee, table, privilege). grantee is a
// role name or the literal "public"; privilege is SELECT/INSERT/UPDATE/DELETE/
// TRUNCATE or "ALL".
const grantsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_grants (
  grantee   TEXT COLLATE NOCASE,
  tablename TEXT COLLATE NOCASE,
  privilege TEXT COLLATE NOCASE
)`

// ownersTableDDL records the role that created each table (its owner, who holds
// all privileges implicitly and alone may drop/alter it).
const ownersTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_owners (
  tablename TEXT PRIMARY KEY COLLATE NOCASE,
  owner     TEXT COLLATE NOCASE
)`

// membershipsTableDDL records role membership (GRANT role TO role): member is a
// member of roleof, and inherits its privileges when member has INHERIT.
// admin_option marks WITH ADMIN OPTION — the right to grant that role on.
const membershipsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_memberships (
  member       TEXT COLLATE NOCASE,
  roleof       TEXT COLLATE NOCASE,
  admin_option INTEGER DEFAULT 0
)`

// membershipsAddAdminDDL adds admin_option to membership tables created before
// WITH ADMIN OPTION existed; it errors (harmlessly, ignored) when present.
const membershipsAddAdminDDL = `ALTER TABLE _overlite_memberships ADD COLUMN admin_option INTEGER DEFAULT 0`
