package engine

// Roles are stored in an internal table `_overlite_roles` in the main database
// (persistent, shared across connections). SQLite has no roles/privileges, so
// this exists purely to make CREATE/ALTER/DROP ROLE and \du behave; GRANT/
// REVOKE are accepted as no-ops by the protocol. The table is hidden from the
// catalog (see the `_overlite_*` GLOB filter in catalog_views.go).

const rolesTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_roles (
  rolname       TEXT PRIMARY KEY COLLATE NOCASE,
  rolsuper      INTEGER DEFAULT 0,
  rolinherit    INTEGER DEFAULT 1,
  rolcreaterole INTEGER DEFAULT 0,
  rolcreatedb   INTEGER DEFAULT 0,
  rolcanlogin   INTEGER DEFAULT 0,
  rolreplication INTEGER DEFAULT 0,
  rolbypassrls  INTEGER DEFAULT 0
)`

// seedDefaultRoleSQL inserts the configured default role (a superuser that can
// log in) if it isn't already present.
func seedDefaultRoleSQL() string {
	return `INSERT OR IGNORE INTO _overlite_roles
	 (rolname, rolsuper, rolinherit, rolcreaterole, rolcreatedb, rolcanlogin, rolbypassrls)
	 VALUES (` + sqlQuote(catalogRole) + `, 1, 1, 1, 1, 1, 1)`
}
