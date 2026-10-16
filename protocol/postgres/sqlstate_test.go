package postgres

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSQLStateFromSQLiteErrors(t *testing.T) {
	cases := map[string]string{
		"constraint failed: UNIQUE constraint failed: users.email (2067)": "23505",
		"FOREIGN KEY constraint failed (787)":                             "23503",
		"NOT NULL constraint failed: users.name (1299)":                   "23502",
		"database is locked (5) (SQLITE_BUSY)":                            "55P03",
		"SQL logic error: no such table: nope (1)":                        "42P01",
		"SQL logic error: no such column: nope (1)":                       "42703",
		`SQL logic error: near "selec": syntax error (1)`:                 "42601",
		"SQL logic error: table users already exists (1)":                 "42P07",
		"something else": "42000",
	}
	for msg, want := range cases {
		assert.Equalf(t, want, sqlState(errors.New(msg)), "sqlState(%q)", msg)
	}
}
