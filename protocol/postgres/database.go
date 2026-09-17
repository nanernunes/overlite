package postgres

import (
	"fmt"
	"strings"
)

// CREATE / DROP DATABASE act on the server's set of databases, not on the one
// the connection is using, so they are handled here rather than reaching the
// engine. A server holding a single file has no set to act on and says so.

// isDatabaseDDL reports whether sql is a CREATE/DROP DATABASE.
func isDatabaseDDL(sql string) bool {
	w := firstWordUpper(sql)
	return (w == "CREATE" || w == "DROP") && secondWordUpper(sql) == "DATABASE"
}

// tryDatabaseDDL runs a CREATE/DROP DATABASE, returning the command tag.
func (s *session) tryDatabaseDDL(sql string) (tag string, handled bool, err error) {
	if !isDatabaseDDL(sql) {
		return "", false, nil
	}
	if s.cluster == nil {
		return "", true, fmt.Errorf("this server does not manage databases")
	}

	create := strings.EqualFold(firstWordUpper(sql), "CREATE")
	name, ifExists, ok := parseDatabaseDDL(sql)
	if !ok {
		return "", true, fmt.Errorf("syntax error in %s DATABASE", firstWordUpper(sql))
	}

	if create {
		if err := s.cluster.CreateDatabase(s.ctx, name); err != nil {
			if ifExists && strings.Contains(err.Error(), "already exists") {
				return "CREATE DATABASE", true, nil
			}
			return "", true, err
		}
		return "CREATE DATABASE", true, nil
	}

	if strings.EqualFold(name, s.database) {
		return "", true, fmt.Errorf("cannot drop the currently open database")
	}
	if err := s.cluster.DropDatabase(s.ctx, name); err != nil {
		if ifExists && strings.Contains(err.Error(), "does not exist") {
			return "DROP DATABASE", true, nil
		}
		return "", true, err
	}
	return "DROP DATABASE", true, nil
}

// parseDatabaseDDL pulls the name out of a CREATE/DROP DATABASE, along with
// whether IF [NOT] EXISTS was given. Options after the name (ENCODING, OWNER,
// WITH ...) are accepted and ignored: there is nothing here to vary.
func parseDatabaseDDL(sql string) (name string, ifExists bool, ok bool) {
	f := strings.Fields(strings.TrimSuffix(strings.TrimSpace(sql), ";"))
	if len(f) < 3 {
		return "", false, false
	}
	rest := f[2:]
	// IF NOT EXISTS / IF EXISTS
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") {
		if strings.EqualFold(rest[1], "not") && len(rest) >= 3 && strings.EqualFold(rest[2], "exists") {
			ifExists, rest = true, rest[3:]
		} else if strings.EqualFold(rest[1], "exists") {
			ifExists, rest = true, rest[2:]
		}
	}
	if len(rest) == 0 {
		return "", false, false
	}
	return unquoteIdent(rest[0]), ifExists, true
}

// databaseList answers a query against pg_database from the cluster, so a
// client sees the databases this server actually holds.
func (s *session) databaseList() ([]string, error) {
	if s.cluster == nil {
		return []string{s.database}, nil
	}
	return s.cluster.Databases()
}

// refreshDatabaseList rebuilds this connection's pg_database so it lists the
// databases the server actually holds.
//
// The engine builds the view knowing only its own database, because a SQLite
// file has no idea it sits beside others. The cluster is what knows, and it
// lives out here, so the view is replaced per connection — and again whenever
// a CREATE or DROP changes the set.
func (s *session) refreshDatabaseList() error {
	names, err := s.databaseList()
	if err != nil || len(names) == 0 {
		return nil // leave the engine's own single-row view in place
	}

	rows := make([]string, len(names))
	for i, name := range names {
		rows[i] = fmt.Sprintf(
			`SELECT %d AS oid, %s AS datname, 10 AS datdba, 6 AS encoding,`+
				` 'c' AS datlocprovider, 'C' AS datcollate, 'C' AS datctype,`+
				` NULL AS daticulocale, NULL AS daticurules, NULL AS datcollversion,`+
				` 0 AS datistemplate, 1 AS datallowconn, -1 AS datconnlimit,`+
				` 0 AS dattablespace, 0 AS datfrozenxid, 0 AS datminmxid,`+
				` NULL AS datacl, 1262 AS tableoid`,
			i+1, sqlStr(name))
	}
	if _, err := s.exec(`DROP VIEW IF EXISTS pg_database`, nil); err != nil {
		return nil
	}
	if _, err := s.exec(`CREATE TEMP VIEW pg_database AS `+strings.Join(rows, " UNION ALL "), nil); err != nil {
		return nil
	}
	return nil
}
