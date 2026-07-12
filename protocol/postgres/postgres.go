// Package postgres implements protocol.Protocol for the PostgreSQL frontend/
// backend wire protocol (v3), backed by any core.Engine.
//
// It supports startup + trust auth, the Simple Query protocol ('Q'), and the
// Extended Query protocol (Parse/Bind/Describe/Execute/Sync), which pgx and
// most drivers use by default. A small dialect layer (see dialect.go) and a
// minimal catalog (see the engine package) let common Postgres SQL run against
// SQLite unchanged.
package postgres

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"

	"overlite/core"
	"overlite/hba"
)

// Protocol is the PostgreSQL wire protocol implementation.
type Protocol struct {
	// password, when non-empty, requires clients to authenticate with it. It
	// follows the official Postgres image's POSTGRES_PASSWORD; empty means
	// trust (any password accepted).
	password string
	// tls, when non-nil, lets clients upgrade the connection with SSL.
	tls *tls.Config
	// configuredAuth is POSTGRES_HOST_AUTH_METHOD (trust/password/md5/
	// scram-sha-256); empty means the default (scram when a password is set).
	configuredAuth string
	// hba, when non-nil, is a pg_hba policy that decides the auth method (and
	// rejections) per connection, overriding configuredAuth.
	hba *hba.Policy
}

// New returns a ready-to-use Postgres protocol, honoring POSTGRES_PASSWORD,
// POSTGRES_HOST_AUTH_METHOD, the TLS environment (see loadTLS), and a pg_hba
// policy from OVERLITE_HBA_DIR (default ".").
func New() *Protocol {
	return &Protocol{
		password:       os.Getenv("POSTGRES_PASSWORD"),
		tls:            loadTLS(),
		configuredAuth: os.Getenv("POSTGRES_HOST_AUTH_METHOD"),
		hba:            loadHBA(),
	}
}

// loadHBA reads the HBA policy from OVERLITE_HBA_DIR (default the working
// directory); a parse error is logged and treated as "no policy".
func loadHBA() *hba.Policy {
	dir := os.Getenv("OVERLITE_HBA_DIR")
	if dir == "" {
		dir = "."
	}
	policy, err := hba.Load(dir)
	if err != nil {
		log.Printf("overlite: ignoring pg_hba in %s: %v", dir, err)
		return nil
	}
	return policy
}

func (p *Protocol) Name() string { return "postgres" }

// DefaultPort is PostgreSQL's conventional port.
func (p *Protocol) DefaultPort() int { return 5432 }

// Serve implements protocol.Protocol.
func (p *Protocol) Serve(ctx context.Context, conn net.Conn, engine core.Engine) error {
	c := newWireConn(conn)

	params, cancelReq, err := c.readStartup(p.tls)
	if err != nil {
		return err
	}
	if cancelReq != nil {
		// A CancelRequest is a bare connection: interrupt the target and close.
		cancelBackend(cancelReq.pid, cancelReq.secret)
		return nil
	}

	// A dedicated engine connection per client, opened before auth so we can look
	// up the connecting role's password.
	db, err := engine.Session(ctx)
	if err != nil {
		_ = c.sendFatal("53300", "too many connections: "+err.Error())
		_ = c.flush()
		return err
	}
	defer db.Close()

	if err := p.authenticate(c, params["user"], params["database"], clientIP(conn), c.secured, db); err != nil {
		return err
	}

	// Assign this connection a backend key so a second connection can cancel
	// its running query.
	pid, secret := randInt32()&0x7fffffff, randInt32() // a positive backend pid
	cl := registerBackend(pid, secret)
	defer unregisterBackend(pid, secret)

	if err := p.completeHandshake(c, pid, secret); err != nil {
		return err
	}

	s := newSession(ctx, c, db, params["user"], pid)
	s.canceler = cl
	return s.loop()
}

// authMethod resolves the auth method for this connection: trust when no
// password is set, otherwise POSTGRES_HOST_AUTH_METHOD (scram-sha-256 by
// default, matching modern Postgres).
func (p *Protocol) authMethod() string {
	if p.password == "" {
		return "trust"
	}
	switch strings.ToLower(p.configuredAuth) {
	case "trust":
		return "trust"
	case "password":
		return "password"
	case "md5":
		return "md5"
	default: // "scram-sha-256", "scram", or unset
		return "scram"
	}
}

// credential is what a password method authenticates against: a stored SCRAM
// verifier (per-role) or a known plaintext (the global POSTGRES_PASSWORD).
type credential struct {
	plaintext string
	scram     *scramVerifier
}

// matches reports whether password satisfies the credential (cleartext path).
func (cred credential) matches(password string) bool {
	if cred.scram != nil {
		return cred.scram.verifies(password)
	}
	return password == cred.plaintext
}

// authenticate runs the auth method resolved for this connection against the
// client (the pg_hba policy decides when configured, else the global method).
func (p *Protocol) authenticate(c *wireConn, user, database string, ip net.IP, ssl bool, sess core.Session) error {
	// A role explicitly marked NOLOGIN can't open a session, whatever the auth
	// method (Postgres checks this before authenticating).
	if !roleMayLogin(sess, user) {
		_ = c.sendFatal("28000", fmt.Sprintf("role %q is not permitted to log in", user))
		_ = c.flush()
		return fmt.Errorf("role %q is not permitted to log in", user)
	}
	method := p.resolveMethod(user, database, ip, ssl)
	if method == "reject" {
		_ = c.sendFatal("28000",
			fmt.Sprintf("no pg_hba.conf entry for user %q, database %q", user, database))
		_ = c.flush()
		return fmt.Errorf("connection rejected by pg_hba: user=%s db=%s", user, database)
	}
	if method == "trust" {
		return nil
	}
	cred, ok := p.credentialFor(sess, user)
	if !ok {
		return p.authFailed(c) // password method but no credential configured
	}
	switch method {
	case "password":
		return p.authCleartext(c, cred)
	case "md5":
		return p.authMD5(c, user, cred)
	default:
		return p.authSCRAM(c, user, cred)
	}
}

// credentialFor resolves the connecting role's credential: its stored SCRAM
// verifier if it has one, otherwise the global POSTGRES_PASSWORD.
func (p *Protocol) credentialFor(sess core.Session, user string) (credential, bool) {
	if v := lookupRoleVerifier(sess, user); v != nil {
		return credential{scram: v}, true
	}
	if p.password != "" {
		return credential{plaintext: p.password}, true
	}
	return credential{}, false
}

// roleMayLogin reports whether the connecting role is allowed to open a session.
// A role is blocked only when it exists in _overlite_roles and its rolcanlogin
// flag is 0 (NOLOGIN); an unknown role (or one predating the flag) is allowed,
// so trust auth and the global password keep working.
func roleMayLogin(sess core.Session, user string) bool {
	rs, err := sess.Execute(context.Background(),
		"SELECT rolcanlogin FROM _overlite_roles WHERE rolname = ? COLLATE NOCASE",
		[]core.Value{user})
	if err != nil || len(rs.Rows) == 0 || rs.Rows[0][0] == nil {
		return true
	}
	return fmt.Sprint(rs.Rows[0][0]) != "0"
}

// lookupRoleVerifier reads a role's stored SCRAM verifier from _overlite_roles.
func lookupRoleVerifier(sess core.Session, user string) *scramVerifier {
	rs, err := sess.Execute(context.Background(),
		"SELECT rolpassword FROM _overlite_roles WHERE rolname = ? COLLATE NOCASE AND rolpassword IS NOT NULL",
		[]core.Value{user})
	if err != nil || len(rs.Rows) == 0 || rs.Rows[0][0] == nil {
		return nil
	}
	v, _ := parseSCRAMVerifier(fmt.Sprint(rs.Rows[0][0]))
	return v
}

// resolveMethod returns the effective auth method for a connection: from the
// pg_hba policy when configured (rejecting unmatched connections), otherwise the
// global POSTGRES_HOST_AUTH_METHOD-derived method.
func (p *Protocol) resolveMethod(user, db string, ip net.IP, ssl bool) string {
	if p.hba == nil {
		return p.authMethod()
	}
	m, ok := p.hba.Method(hba.Conn{Local: ip == nil, SSL: ssl, Database: db, User: user, IP: ip})
	if !ok {
		return "reject"
	}
	return normalizeHBAMethod(m)
}

// normalizeHBAMethod maps a pg_hba method name to one overlite implements. Peer/
// cert are accepted without their (unsupported) verification; external methods
// (ldap/gss/…) require a password, so they fall back to scram.
func normalizeHBAMethod(m string) string {
	switch strings.ToLower(m) {
	case "trust", "reject", "md5", "password":
		return strings.ToLower(m)
	case "scram-sha-256", "scram":
		return "scram"
	case "peer", "cert":
		return "trust"
	default:
		return "scram"
	}
}

// clientIP extracts the client's IP from a TCP connection, or nil for a local
// (Unix-socket) connection.
func clientIP(conn net.Conn) net.IP {
	if a, ok := conn.RemoteAddr().(*net.TCPAddr); ok {
		return a.IP
	}
	return nil
}

// authFailed reports the standard auth failure to the client.
func (p *Protocol) authFailed(c *wireConn) error {
	_ = c.sendFatal("28P01", "password authentication failed")
	_ = c.flush()
	return fmt.Errorf("password authentication failed")
}

// authCleartext performs AuthenticationCleartextPassword (code 3) against cred.
func (p *Protocol) authCleartext(c *wireConn, cred credential) error {
	if err := c.send(msgAuthentication, i32(3)); err != nil {
		return err
	}
	if err := c.flush(); err != nil {
		return err
	}
	typ, body, err := c.readMessage()
	if err != nil {
		return err
	}
	if typ != msgPasswordMessage {
		return fmt.Errorf("expected password message, got %q", string(typ))
	}
	if !cred.matches(strings.TrimRight(string(body), "\x00")) {
		return p.authFailed(c)
	}
	return nil
}

// authMD5 performs AuthenticationMD5Password (code 5): the client returns
// "md5" + md5(md5(password+user) + salt), which we recompute and compare. It
// needs the plaintext, so a role stored only as a SCRAM verifier can't use md5
// (Postgres behaves the same).
func (p *Protocol) authMD5(c *wireConn, user string, cred credential) error {
	salt := make([]byte, 4)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	if err := c.send(msgAuthentication, append(i32(5), salt...)); err != nil {
		return err
	}
	if err := c.flush(); err != nil {
		return err
	}
	typ, body, err := c.readMessage()
	if err != nil {
		return err
	}
	if typ != msgPasswordMessage {
		return fmt.Errorf("expected password message, got %q", string(typ))
	}
	if cred.plaintext == "" ||
		strings.TrimRight(string(body), "\x00") != md5Password(cred.plaintext, user, salt) {
		return p.authFailed(c)
	}
	return nil
}

// md5Password computes the Postgres MD5 auth response for a password/user/salt.
func md5Password(password, user string, salt []byte) string {
	inner := md5Hex([]byte(password + user))
	return "md5" + md5Hex(append([]byte(inner), salt...))
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

// completeHandshake sends AuthenticationOk (trust), a few parameter statuses,
// and the first ReadyForQuery.
func (p *Protocol) completeHandshake(c *wireConn, pid, secret int32) error {
	// AuthenticationOk: int32(0).
	if err := c.send(msgAuthentication, i32(0)); err != nil {
		return err
	}
	for k, v := range map[string]string{
		"server_version":              "15.0 (overlite)",
		"server_encoding":             "UTF8",
		"client_encoding":             "UTF8",
		"DateStyle":                   "ISO, MDY",
		"integer_datetimes":           "on",
		"standard_conforming_strings": "on", // required by pgx's simple protocol
	} {
		if err := c.sendParameterStatus(k, v); err != nil {
			return err
		}
	}
	// BackendKeyData: the pid + secret a CancelRequest must echo to cancel this
	// connection's running query.
	if err := c.send(msgBackendKeyData, append(i32(pid), i32(secret)...)); err != nil {
		return err
	}
	if err := c.sendReadyForQuery(); err != nil {
		return err
	}
	return c.flush()
}

func (s *session) handleSimpleQuery(body []byte) error {
	sql := strings.TrimRight(string(body), "\x00")
	sql = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))

	if sql == "" {
		return s.c.send(msgEmptyQuery, nil)
	}

	// Transaction control is handled by the session (real BEGIN/COMMIT/ROLLBACK).
	if kind := txControlKind(sql); kind != "" {
		tag, err := s.applyTxControl(kind)
		if err != nil {
			s.txFailed = s.tx != nil
			return s.c.sendError("25000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.trySchemaDDL(sql); handled {
		if err != nil {
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	// Savepoints (incl. ROLLBACK TO, which recovers an aborted tx) are handled
	// before the aborted-tx guard.
	if handled, tag, code, err := s.trySavepoint(sql); handled {
		if err != nil {
			return s.c.sendError(code, err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	// Reject non-tx-control statements while the transaction is aborted.
	if s.abortedTxError() {
		return s.c.sendError("25P02",
			"current transaction is aborted, commands ignored until end of transaction block")
	}

	if tag, handled, err := s.tryRoleDDL(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		s.clearPrivCache() // superuser/managed status may have changed
		return s.c.sendCommandComplete(tag)
	}

	if isGrant(sql) {
		tag, err := s.applyGrant(sql)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.tryRLSDDL(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.tryAlterTable(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.sendExecError(err)
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled := s.tryListenNotify(sql); handled {
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.trySequenceDDL(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.tryTypeDDL(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if tag, handled, err := s.tryComment(sql); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if isSetRole(sql) {
		if err := s.applySetRole(sql); err != nil {
			return s.c.sendError("22023", err.Error())
		}
		return s.c.sendCommandComplete(firstWordUpper(sql))
	}

	if handled, err := s.trySQLPrepare(sql); handled {
		return err
	}

	if cs, ok := parseCopy(sql); ok {
		return s.handleCopy(cs)
	}

	if rs, ok := interceptUtility(sql); ok {
		if rs.IsQuery {
			if err := s.c.sendResultSet(rs); err != nil {
				return err
			}
		}
		return s.c.sendCommandComplete(commandTag(rs))
	}

	if err := s.checkPrivileges(sql); err != nil {
		if s.tx != nil {
			s.txFailed = true
		}
		return s.c.sendError("42501", err.Error())
	}

	// Record/strip nextval column defaults (CREATE TABLE) or inject them (INSERT).
	sql = s.applyColumnDefaults(sql)

	if handled, rs, err := s.tryRLSInsert(sql, nil); handled {
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.c.sendError("42501", err.Error())
		}
		if rs.IsQuery { // DO UPDATE … RETURNING passed the row check
			if err := s.c.sendResultSet(rs); err != nil {
				return err
			}
		}
		return s.c.sendCommandComplete(commandTag(rs))
	}

	// Expand sequence calls and enum columns on the raw statement, before the
	// dialect rewrite (see rewriteForExec).
	rewritten, err := s.rewriteForExec(sql)
	if err != nil {
		if s.tx != nil {
			s.txFailed = true
		}
		return s.c.sendError("42P01", err.Error())
	}
	logQuery("simple", rewritten)
	rs, err := s.exec(rewritten, nil)
	if err != nil {
		logQueryError("simple", err, sql, rewritten)
		if s.tx != nil {
			s.txFailed = true
		}
		return s.sendExecError(err)
	}
	s.recordOwnership(sql)
	if rs.IsQuery {
		if err := s.c.sendResultSet(rs); err != nil {
			return err
		}
	}
	return s.c.sendCommandComplete(commandTag(rs))
}

// --- backend message builders -------------------------------------------------

func (c *wireConn) sendParameterStatus(key, val string) error {
	var b []byte
	b = appendCString(b, key)
	b = appendCString(b, val)
	return c.send(msgParameterStatus, b)
}

func (c *wireConn) sendReadyForQuery() error {
	// 'I' = idle (not in a transaction).
	return c.send(msgReadyForQuery, []byte{'I'})
}

// sendResultSet emits a RowDescription followed by the DataRows, as the Simple
// Query protocol requires.
func (c *wireConn) sendResultSet(rs *core.ResultSet) error {
	oids := make([]uint32, len(rs.Columns))
	for i, col := range rs.Columns {
		oids[i] = oidForColumn(col, rs.Rows, i)
	}
	if err := c.sendRowDescription(rs.Columns, oids); err != nil {
		return err
	}
	// Simple Query always uses text format for results.
	return c.sendDataRows(rs.Rows, oids, nil)
}

// sendRowDescription describes result columns. All columns use text format.
func (c *wireConn) sendRowDescription(cols []core.Column, oids []uint32) error {
	var t []byte
	t = appendInt16(t, len(cols))
	for i, col := range cols {
		t = appendCString(t, col.Name)
		t = appendInt32(t, 0)        // table OID
		t = appendInt16(t, 0)        // column attribute number
		t = appendUint32(t, oids[i]) // type OID
		t = appendInt16(t, -1)       // type size (variable)
		t = appendInt32(t, -1)       // type modifier
		t = appendInt16(t, 0)        // format code: 0 = text
	}
	return c.send(msgRowDescription, t)
}

// sendDataRows emits one DataRow per row. Each column is encoded in the format
// the client requested for it (formats; nil means all-text).
func (c *wireConn) sendDataRows(rows [][]core.Value, oids []uint32, formats []int) error {
	for _, row := range rows {
		var d []byte
		d = appendInt16(d, len(row))
		for i, cell := range row {
			enc := encodeValue(formatFor(formats, i), oids[i], cell)
			if enc == nil {
				d = appendInt32(d, -1) // NULL
				continue
			}
			d = appendInt32(d, len(enc))
			d = append(d, enc...)
		}
		if err := c.send(msgDataRow, d); err != nil {
			return err
		}
	}
	return nil
}

// sendParameterDescription advertises n parameters, all with unspecified type
// (OID 0). That makes clients send parameter values in text format, which we
// decode trivially and let SQLite coerce.
func (c *wireConn) sendParameterDescription(n int) error {
	var b []byte
	b = appendInt16(b, n)
	for i := 0; i < n; i++ {
		b = appendUint32(b, 0)
	}
	return c.send(msgParameterDescription, b)
}

func (c *wireConn) sendCommandComplete(tag string) error {
	return c.send(msgCommandComplete, appendCString(nil, tag))
}

func (c *wireConn) sendError(code, message string) error {
	return c.sendErrorSeverity("ERROR", code, message)
}

func (c *wireConn) sendFatal(code, message string) error {
	return c.sendErrorSeverity("FATAL", code, message)
}

func (c *wireConn) sendErrorSeverity(severity, code, message string) error {
	var b []byte
	b = append(b, 'S')
	b = appendCString(b, severity)
	b = append(b, 'C')
	b = appendCString(b, code)
	b = append(b, 'M')
	b = appendCString(b, message)
	b = append(b, 0) // terminator
	return c.send(msgErrorResponse, b)
}

// commandTag builds the CommandComplete tag, e.g. "SELECT 3", "INSERT 0 1".
func commandTag(rs *core.ResultSet) string {
	switch rs.Command {
	case "SELECT":
		return "SELECT " + strconv.FormatInt(rs.RowsAffected, 10)
	case "INSERT":
		return "INSERT 0 " + strconv.FormatInt(rs.RowsAffected, 10)
	case "UPDATE", "DELETE":
		return rs.Command + " " + strconv.FormatInt(rs.RowsAffected, 10)
	case "":
		return "OK"
	default:
		return rs.Command
	}
}

// --- little-endian-free encoding helpers -------------------------------------

func i32(v int32) []byte { return appendInt32(nil, int(v)) }

func appendInt16(b []byte, v int) []byte {
	return append(b, byte(uint16(v)>>8), byte(uint16(v)))
}

func appendUint16(b []byte, u uint16) []byte {
	return append(b, byte(u>>8), byte(u))
}

func appendUint64(b []byte, u uint64) []byte {
	return append(b,
		byte(u>>56), byte(u>>48), byte(u>>40), byte(u>>32),
		byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
}

func appendInt32(b []byte, v int) []byte {
	u := uint32(v)
	return append(b, byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
}

func appendUint32(b []byte, u uint32) []byte {
	return append(b, byte(u>>24), byte(u>>16), byte(u>>8), byte(u))
}

func appendCString(b []byte, s string) []byte {
	b = append(b, s...)
	return append(b, 0)
}
