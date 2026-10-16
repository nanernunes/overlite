package postgres

import "strings"

/*
sqlState is the Postgres error code for an error SQLite raised. Drivers and
ORMs act on the code, not the message: GORM turns 23505 into its
ErrDuplicatedKey, a retry loop waits out 55P03, a migration tool tells a
missing table (42P01) from bad syntax (42601). Everything used to be 42000,
which says only "something about the statement".
*/
func sqlState(err error) string {
	msg := strings.ToLower(err.Error())
	for _, c := range sqlStates {
		if strings.Contains(msg, c.text) {
			return c.code
		}
	}
	return "42000"
}

var sqlStates = []struct{ text, code string }{
	{"unique constraint failed", "23505"},
	{"foreign key constraint failed", "23503"},
	{"not null constraint failed", "23502"},
	{"check constraint failed", "23514"},
	{"database is locked", "55P03"},
	{"database table is locked", "55P03"},
	{"no such table", "42P01"},
	{"no such column", "42703"},
	{"no such function", "42883"},
	{"already exists", "42P07"},
	{"syntax error", "42601"},
	{"datatype mismatch", "42804"},
	{"division by zero", "22012"},
}
