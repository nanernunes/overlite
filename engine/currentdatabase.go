package engine

import "strings"

// current_database() names the database the connection is on, which a process
// serving several of them cannot answer from a globally registered SQLite
// function. It is resolved per statement instead, like current_schema().

// resolveCurrentDatabase replaces current_database() with this database's name.
func resolveCurrentDatabase(st *dbState, query string) string {
	if !strings.Contains(strings.ToLower(query), "current_database") {
		return query
	}
	name := sqlQuote(st.dbName())
	return replaceCallOutsideStrings(query, "current_database", func(string) string {
		return name
	})
}
