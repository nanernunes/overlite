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
	"net"
	"os"
	"strconv"
	"strings"

	"overlite/core"
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
	// scram-sha-256); empty means the default (md5 when a password is set).
	configuredAuth string
}

// New returns a ready-to-use Postgres protocol, honoring POSTGRES_PASSWORD,
// POSTGRES_HOST_AUTH_METHOD, and the TLS environment (see loadTLS).
func New() *Protocol {
	return &Protocol{
		password:       os.Getenv("POSTGRES_PASSWORD"),
		tls:            loadTLS(),
		configuredAuth: os.Getenv("POSTGRES_HOST_AUTH_METHOD"),
	}
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
	if err := p.authenticate(c, params["user"]); err != nil {
		return err
	}

	// Assign this connection a backend key so a second connection can cancel
	// its running query.
	pid, secret := randInt32(), randInt32()
	cl := registerBackend(pid, secret)
	defer unregisterBackend(pid, secret)

	if err := p.completeHandshake(c, pid, secret); err != nil {
		return err
	}

	// A dedicated engine connection per client, so clients run concurrently.
	db, err := engine.Session(ctx)
	if err != nil {
		_ = c.sendFatal("53300", "too many connections: "+err.Error())
		_ = c.flush()
		return err
	}
	defer db.Close()

	s := newSession(ctx, c, db)
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

// authenticate runs the resolved auth method against the client.
func (p *Protocol) authenticate(c *wireConn, user string) error {
	switch p.authMethod() {
	case "trust":
		return nil
	case "password":
		return p.authCleartext(c)
	case "md5":
		return p.authMD5(c, user)
	default:
		return p.authSCRAM(c, user)
	}
}

// authFailed reports the standard auth failure to the client.
func (p *Protocol) authFailed(c *wireConn) error {
	_ = c.sendFatal("28P01", "password authentication failed")
	_ = c.flush()
	return fmt.Errorf("password authentication failed")
}

// authCleartext performs AuthenticationCleartextPassword (code 3).
func (p *Protocol) authCleartext(c *wireConn) error {
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
	if strings.TrimRight(string(body), "\x00") != p.password {
		return p.authFailed(c)
	}
	return nil
}

// authMD5 performs AuthenticationMD5Password (code 5): the client returns
// "md5" + md5(md5(password+user) + salt), which we recompute and compare.
func (p *Protocol) authMD5(c *wireConn, user string) error {
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
	if strings.TrimRight(string(body), "\x00") != md5Password(p.password, user, salt) {
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
