package postgres

import (
	"fmt"
	"log"
	"os"
	"strings"

	"overlite/core"
)

// debugQueries enables logging of the SQL behind failed queries (set
// OVERLITE_DEBUG=1). Useful for reproducing a real client's exact statements.
var debugQueries = os.Getenv("OVERLITE_DEBUG") != ""

// debugAll (OVERLITE_DEBUG=all) additionally logs every executed query.
var debugAll = os.Getenv("OVERLITE_DEBUG") == "all"

func logQuery(phase, sql string) {
	if debugAll {
		log.Printf("[%s] %s", phase, sql)
	}
}

func logQueryError(phase string, err error, original, rewritten string) {
	if !debugQueries {
		return
	}
	if original == rewritten {
		log.Printf("[%s] query error: %v\n  sql: %s", phase, err, original)
		return
	}
	log.Printf("[%s] query error: %v\n  sql:       %s\n  rewritten: %s", phase, err, original, rewritten)
}

// interceptUtility handles statements that clients (psql, the JDBC driver
// DBeaver uses, ...) expect to succeed but SQLite can't run: session control
// (SET/RESET/DISCARD), transaction control (BEGIN/COMMIT/ROLLBACK), and SHOW.
//
// It returns a synthetic result and true when it handled the statement. This
// keeps a driver's connection setup working without a real GUC or transaction
// engine; transaction control is a no-op for now (writes autocommit).
func interceptUtility(sql string) (*core.ResultSet, bool) {
	w := firstWordUpper(sql)
	switch w {
	case "SET", "RESET", "DISCARD", "LISTEN", "UNLISTEN", "NOTIFY", "DEALLOCATE",
		"LOAD", "CHECKPOINT", "CLUSTER", "ANALYZE", "LOCK",
		"GRANT", "REVOKE", // SQLite has no per-object privileges; accept as a no-op
		"COMMENT": // no catalog comments yet; accept so migrations/dumps run
		return &core.ResultSet{Command: w}, true
	case "SHOW":
		return showResult(sql), true
	case "CREATE", "DROP":
		// CREATE/DROP EXTENSION: we ship no extensions, but accept it so scripts
		// that enable uuid-ossp/pgcrypto/etc. run (the functions they add, like
		// gen_random_uuid, are provided directly).
		if secondWordUpper(sql) == "EXTENSION" {
			return &core.ResultSet{Command: w + " EXTENSION"}, true
		}
	}
	return nil, false
}

// secondWordUpper returns the upper-cased second whitespace-delimited word.
func secondWordUpper(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) < 2 {
		return ""
	}
	return strings.ToUpper(strings.TrimRight(fields[1], ";"))
}

// txControlKind classifies transaction-control statements (handled by the
// session, which owns the real transaction), or "" for anything else.
// "ROLLBACK TO [SAVEPOINT] x" is a savepoint op, not a full rollback.
func txControlKind(sql string) string {
	if savepointKind(sql) != "" {
		return ""
	}
	switch firstWordUpper(sql) {
	case "BEGIN", "START":
		return "BEGIN"
	case "COMMIT", "END":
		return "COMMIT"
	case "ROLLBACK", "ABORT":
		return "ROLLBACK"
	}
	return ""
}

// savepointKind classifies SAVEPOINT / RELEASE / ROLLBACK TO statements, or ""
// for anything else. SQLite understands the syntax; the session just routes them
// to the active transaction.
func savepointKind(sql string) string {
	fields := strings.Fields(sql)
	if len(fields) == 0 {
		return ""
	}
	switch strings.ToUpper(fields[0]) {
	case "SAVEPOINT":
		return "SAVEPOINT"
	case "RELEASE":
		return "RELEASE"
	case "ROLLBACK", "ABORT":
		for _, f := range fields[1:] {
			if strings.EqualFold(f, "to") {
				return "ROLLBACK TO"
			}
		}
	}
	return ""
}

// trySavepoint handles SAVEPOINT/RELEASE/ROLLBACK TO on the active transaction.
// handled=false means it was not a savepoint statement. When handled and errcode
// is non-empty, the caller sends that error; otherwise it sends CommandComplete
// with tag. ROLLBACK TO recovers an aborted transaction (clears the failed flag).
func (s *session) trySavepoint(sql string) (handled bool, tag, errcode string, err error) {
	kind := savepointKind(sql)
	if kind == "" {
		return false, "", "", nil
	}
	if s.tx == nil {
		return true, "", "25P01", fmt.Errorf("%s can only be used in transaction blocks", kind)
	}
	if kind == "ROLLBACK TO" {
		if _, e := s.tx.Execute(s.ctx, sql, nil); e != nil {
			return true, "", "42000", e
		}
		s.txFailed = false // recovered to the savepoint
		return true, "ROLLBACK", "", nil
	}
	if s.txFailed {
		return true, "", "25P02",
			fmt.Errorf("current transaction is aborted, commands ignored until end of transaction block")
	}
	if _, e := s.tx.Execute(s.ctx, sql, nil); e != nil {
		s.txFailed = true
		return true, "", "42000", e
	}
	return true, kind, "", nil
}

// trySchemaDDL handles CREATE/DROP SCHEMA by delegating to the engine's
// SchemaManager (schemas map to attached SQLite files). It returns handled=true
// when it recognized the statement.
func (s *session) trySchemaDDL(sql string) (tag string, handled bool, err error) {
	fields := strings.Fields(sql)
	if len(fields) < 3 || !strings.EqualFold(fields[1], "schema") {
		return "", false, nil
	}
	sm, ok := s.db.(core.SchemaManager)
	if !ok {
		return "", false, nil
	}
	if s.tx != nil {
		// ATTACH/DETACH (which back schema create/drop) can't run inside a
		// SQLite transaction.
		return "", true, fmt.Errorf("CREATE/DROP SCHEMA cannot run inside a transaction block")
	}
	rest := fields[2:]
	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		name, ifNotExists := parseCreateSchema(rest)
		return "CREATE SCHEMA", true, sm.CreateSchema(s.ctx, name, ifNotExists)
	case "DROP":
		name, ifExists, cascade := parseDropSchema(rest)
		return "DROP SCHEMA", true, sm.DropSchema(s.ctx, name, ifExists, cascade)
	}
	return "", false, nil
}

func parseCreateSchema(rest []string) (name string, ifNotExists bool) {
	if len(rest) >= 3 && strings.EqualFold(rest[0], "if") &&
		strings.EqualFold(rest[1], "not") && strings.EqualFold(rest[2], "exists") {
		ifNotExists = true
		rest = rest[3:]
	}
	if len(rest) > 0 {
		name = unquoteIdent(rest[0])
	}
	return name, ifNotExists
}

func parseDropSchema(rest []string) (name string, ifExists, cascade bool) {
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		ifExists = true
		rest = rest[2:]
	}
	if len(rest) > 0 {
		name = unquoteIdent(rest[0])
	}
	for _, f := range rest {
		if strings.EqualFold(strings.TrimRight(f, ";"), "cascade") {
			cascade = true
		}
	}
	return name, ifExists, cascade
}

func unquoteIdent(s string) string {
	s = strings.TrimRight(s, ";")
	s = strings.TrimSuffix(strings.TrimPrefix(s, `"`), `"`)
	return s
}

func firstWordUpper(sql string) string {
	sql = strings.TrimLeft(sql, " \t\r\n(")
	i := strings.IndexAny(sql, " \t\r\n(;")
	if i < 0 {
		i = len(sql)
	}
	return strings.ToUpper(sql[:i])
}

// showResult builds the one-row result a SHOW returns, with the column named
// after the setting (as PostgreSQL does).
func showResult(sql string) *core.ResultSet {
	name := parseShowName(sql)
	return &core.ResultSet{
		IsQuery:      true,
		Columns:      []core.Column{{Name: name, DeclType: "text"}},
		Rows:         [][]core.Value{{showValue(name)}},
		RowsAffected: 1,
		Command:      "SHOW",
	}
}

func parseShowName(sql string) string {
	fields := strings.Fields(strings.TrimRight(strings.TrimSpace(sql), ";"))
	if len(fields) < 2 {
		return "?column?"
	}
	rest := strings.ToLower(strings.Join(fields[1:], " "))
	if rest == "transaction isolation level" {
		return "transaction_isolation"
	}
	return strings.ToLower(strings.TrimRight(fields[1], ";"))
}

// showValue returns a plausible value for a GUC. Unknown settings return an
// empty string, which is what a client gets for an unset parameter.
func showValue(name string) string {
	switch name {
	case "search_path":
		return `"$user", public`
	case "server_version":
		return "15.0"
	case "server_version_num":
		return "150000"
	case "server_encoding", "client_encoding":
		return "UTF8"
	case "standard_conforming_strings", "integer_datetimes", "is_superuser":
		return "on"
	case "transaction_isolation", "default_transaction_isolation":
		return "read committed"
	case "transaction_read_only", "default_transaction_read_only", "in_hot_standby":
		return "off"
	case "datestyle", "DateStyle":
		return "ISO, MDY"
	case "timezone", "TimeZone":
		return "UTC"
	case "max_index_keys":
		return "32"
	case "max_identifier_length":
		return "63"
	case "application_name":
		return "overlite"
	default:
		return ""
	}
}
