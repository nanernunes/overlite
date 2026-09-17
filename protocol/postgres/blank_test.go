package postgres

import "testing"

func TestIsBlankStatement(t *testing.T) {
	blank := []string{
		"",
		"   ",
		"\t\n",
		";",
		" ; ; ",
		"-- ping", // database/sql's Ping
		"--",
		"-- ping;",
		"--ping\n--again",
		"/* c */",
		"/* multi\nline */",
		"/* outer /* inner */ */", // Postgres nests block comments
		" -- one\n /* two */ ; ",
	}
	for _, sql := range blank {
		if !isBlankStatement(sql) {
			t.Errorf("isBlankStatement(%q) = false, want true", sql)
		}
	}

	executable := []string{
		"SELECT 1",
		"-- ping\nSELECT 1",
		"/* c */ SELECT 1",
		"SELECT 1 -- trailing",
		"SELECT '-- not a comment'",
		"SELECT '/* not a comment */'",
		`SELECT "--"`,
		"/* unterminated",
	}
	for _, sql := range executable {
		if isBlankStatement(sql) {
			t.Errorf("isBlankStatement(%q) = true, want false", sql)
		}
	}
}
