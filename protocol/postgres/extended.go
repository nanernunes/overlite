package postgres

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"

	"overlite/core"
)

// session holds the per-connection state that the Extended Query protocol
// needs: named prepared statements and bound portals. A Simple Query ('Q')
// borrows the same senders but keeps no state.
type session struct {
	ctx context.Context
	c   *wireConn
	db  core.Session // this client's dedicated engine connection

	prepared map[string]*prepared
	portals  map[string]*portal

	// failed is set when a message in an extended request cycle errors; the
	// rest of the cycle is skipped until the next Sync, per the protocol.
	failed bool

	// tx is the current transaction (nil = autocommit). txFailed marks it as
	// aborted: further statements are rejected until COMMIT/ROLLBACK.
	tx       core.Tx
	txFailed bool

	// Sequence state scoped to this session: seqCurr is the last value produced
	// per sequence (backs currval), seqLast is the most recent one (backs
	// lastval). Keys are lower-cased sequence names.
	seqCurr map[string]int64
	seqLast string

	// canceler interrupts this connection's in-flight query on a CancelRequest.
	canceler *canceler

	// sqlPrepared holds SQL-level prepared statements (PREPARE name AS ...),
	// which psql/pg_dump use; distinct from the wire-protocol prepared map.
	sqlPrepared map[string]string

	// Session identity. authUser is the role that logged in (immutable);
	// sessionUser (session_user) changes with SET SESSION AUTHORIZATION;
	// currentRole (current_user) changes with SET ROLE.
	authUser    string
	sessionUser string
	currentRole string

	// bypassCache memoizes whether a role skips privilege checks (superuser or
	// unmanaged), keyed by role name; cleared on role DDL.
	bypassCache map[string]bool

	// LISTEN/NOTIFY state. pid is this backend's id (sent in a Notificationsent);
	// listens is the set of channels; pending holds notifications to flush; idle
	// is true when the session is between commands (safe to deliver). nmu guards
	// pending/idle and serializes connection writes with a NOTIFYing goroutine.
	pid     int32
	listens map[string]bool
	pending []pgNotify
	idle    bool
	nmu     sync.Mutex
}

type prepared struct {
	sql       string // rewritten, ready for the engine
	raw       string // original statement, kept for sequence expansion (pre-rewrite)
	numParams int
	// util is set for intercepted statements (SET/SHOW/...) that bypass the
	// engine and return a synthetic result.
	util *core.ResultSet
	// txControl is "BEGIN"/"COMMIT"/"ROLLBACK" for transaction-control
	// statements, else "".
	txControl string
	// seqDDL holds the raw CREATE/ALTER/DROP SEQUENCE statement, run at Execute.
	seqDDL string
	// typeDDL holds the raw CREATE/ALTER/DROP TYPE statement, run at Execute.
	typeDDL string
	// setRole holds a raw SET/RESET ROLE / SESSION AUTHORIZATION statement.
	setRole string
	// grant holds a raw GRANT/REVOKE statement, applied at Execute.
	grant string
	// rlsDDL holds a raw RLS DDL statement (ALTER TABLE … RLS / CREATE POLICY).
	rlsDDL string
	// alterDDL holds a raw ALTER TABLE we implement (rebuild / unique index).
	alterDDL string
	// listenNotify holds a raw LISTEN/UNLISTEN/NOTIFY statement.
	listenNotify string
}

type portal struct {
	prep    *prepared
	params  []core.Value
	formats []int // result-column format codes from Bind

	// result and pos support partial fetch (Execute's max-rows): the query runs
	// once, then successive Executes page through the buffered rows, emitting
	// PortalSuspended until they are exhausted.
	result *core.ResultSet
	pos    int
}

func newSession(ctx context.Context, c *wireConn, db core.Session, user string, pid int32) *session {
	if user == "" {
		user = "postgres"
	}
	return &session{
		ctx:         ctx,
		c:           c,
		db:          db,
		prepared:    map[string]*prepared{},
		portals:     map[string]*portal{},
		seqCurr:     map[string]int64{},
		sqlPrepared: map[string]string{},
		authUser:    user,
		sessionUser: user,
		currentRole: user,
		pid:         pid,
		listens:     map[string]bool{},
	}
}

// rewriteForExec applies the session-level expansions (sequence calls, enum
// columns) to the raw statement, then the dialect rewrite. The expansions run
// first so the integer/label literals they inject are protected by the
// string-literal-aware rewrite.
func (s *session) rewriteForExec(raw string) (string, error) {
	x, err := s.expandSequences(s.expandSessionUser(s.applyRLS(raw)))
	if err != nil {
		return "", err
	}
	x, err = s.expandEnums(x)
	if err != nil {
		return "", err
	}
	return rewrite(x), nil
}

// exec runs a statement in the current transaction, or autocommit if none,
// under a cancellable context so a CancelRequest can interrupt it.
func (s *session) exec(sql string, args []core.Value) (*core.ResultSet, error) {
	ctx, cancel := s.armCancel()
	defer cancel()
	if s.tx != nil {
		return s.tx.Execute(ctx, sql, args)
	}
	return s.db.Execute(ctx, sql, args)
}

// armCancel derives a cancellable context from the session and registers its
// cancel with the connection's canceler for the duration of one statement.
func (s *session) armCancel() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(s.ctx)
	if s.canceler != nil {
		s.canceler.arm(cancel)
	}
	return ctx, func() {
		if s.canceler != nil {
			s.canceler.disarm()
		}
		cancel()
	}
}

func (s *session) describeSQL(sql string, args []core.Value) ([]core.Column, error) {
	if s.tx != nil {
		return s.tx.Describe(s.ctx, sql, args)
	}
	return s.db.Describe(s.ctx, sql, args)
}

// readyForQuery reports the transaction status: 'I' idle, 'T' in a transaction,
// 'E' in a failed (aborted) transaction.
func (s *session) readyForQuery() error {
	status := byte('I')
	if s.tx != nil {
		if s.txFailed {
			status = 'E'
		} else {
			status = 'T'
		}
	}
	return s.c.send(msgReadyForQuery, []byte{status})
}

// applyTxControl performs BEGIN/COMMIT/ROLLBACK and returns the command tag.
func (s *session) applyTxControl(kind string) (string, error) {
	switch kind {
	case "BEGIN":
		if s.tx == nil {
			tx, err := s.db.Begin(s.ctx)
			if err != nil {
				return "BEGIN", err
			}
			s.tx = tx
			s.txFailed = false
		}
		return "BEGIN", nil
	case "COMMIT":
		return "COMMIT", s.endTx(true)
	case "ROLLBACK":
		return "ROLLBACK", s.endTx(false)
	}
	return "", nil
}

// endTx commits (or rolls back) the current transaction and clears state. A
// failed transaction always rolls back, even on COMMIT.
func (s *session) endTx(commit bool) error {
	if s.tx == nil {
		return nil // no transaction in progress; harmless no-op
	}
	tx := s.tx
	s.tx = nil
	failed := s.txFailed
	s.txFailed = false
	if commit && !failed {
		return tx.Commit()
	}
	return tx.Rollback()
}

// abortedTxError guards statements issued while a transaction is aborted.
func (s *session) abortedTxError() bool { return s.tx != nil && s.txFailed }

// loop reads and dispatches frontend messages until the client disconnects.
func (s *session) loop() error {
	// A client that drops mid-transaction leaves it uncommitted; roll it back,
	// and stop receiving notifications.
	defer func() {
		unregisterAll(s)
		if s.tx != nil {
			_ = s.tx.Rollback()
			s.tx = nil
		}
	}()

	for {
		typ, body, err := s.c.readMessage()
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.goBusy() // a NOTIFY may only write to the connection while we're idle

		switch typ {
		case msgQuery:
			s.failed = false
			if err := s.handleSimpleQuery(body); err != nil {
				return err
			}
			if err := s.readyForQuery(); err != nil {
				return err
			}
			if err := s.c.flush(); err != nil {
				return err
			}
			s.goIdle()

		case msgParse, msgBind, msgDescribe, msgExecute, msgClose:
			// Extended request cycle: buffer responses, no ReadyForQuery until
			// Sync. Skip the rest of a failed cycle.
			if s.failed {
				continue
			}
			if err := s.dispatchExtended(typ, body); err != nil {
				return err
			}

		case msgSync:
			s.failed = false
			if err := s.readyForQuery(); err != nil {
				return err
			}
			if err := s.c.flush(); err != nil {
				return err
			}
			s.goIdle()

		case msgFlush:
			if err := s.c.flush(); err != nil {
				return err
			}

		case msgTerminate:
			return nil

		default:
			if err := s.protoError("0A000", "unsupported message '"+string(typ)+"'"); err != nil {
				return err
			}
		}
	}
}

func (s *session) dispatchExtended(typ byte, body []byte) error {
	switch typ {
	case msgParse:
		return s.handleParse(body)
	case msgBind:
		return s.handleBind(body)
	case msgDescribe:
		return s.handleDescribe(body)
	case msgExecute:
		return s.handleExecute(body)
	case msgClose:
		return s.handleClose(body)
	}
	return nil
}

// protoError reports a per-cycle error and arms the skip-until-Sync flag. It
// returns a non-nil error only if writing the ErrorResponse itself failed.
func (s *session) protoError(code, msg string) error {
	s.failed = true
	return s.c.sendError(code, msg)
}

func (s *session) handleParse(body []byte) error {
	r := newReader(body)
	name := r.cstring()
	query := r.cstring()
	nOIDs := r.int16()
	for i := 0; i < nOIDs; i++ {
		r.int32() // client-supplied type hints; ignored (we advertise text)
	}
	if r.err != nil {
		return s.protoError("08P01", "malformed Parse message")
	}

	raw := trimStatement(query)
	if kind := txControlKind(raw); kind != "" {
		s.prepared[name] = &prepared{txControl: kind}
		return s.c.send(msgParseComplete, nil)
	}
	if alterTableHandled(raw) {
		s.prepared[name] = &prepared{alterDDL: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if isListenNotify(raw) {
		s.prepared[name] = &prepared{listenNotify: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if rs, ok := interceptUtility(raw); ok {
		s.prepared[name] = &prepared{util: rs}
		return s.c.send(msgParseComplete, nil)
	}
	if isSequenceDDL(raw) {
		s.prepared[name] = &prepared{seqDDL: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if isTypeDDL(raw) {
		s.prepared[name] = &prepared{typeDDL: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if isSetRole(raw) {
		s.prepared[name] = &prepared{setRole: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if isGrant(raw) {
		s.prepared[name] = &prepared{grant: raw}
		return s.c.send(msgParseComplete, nil)
	}
	if isRLSDDL(raw) {
		s.prepared[name] = &prepared{rlsDDL: raw}
		return s.c.send(msgParseComplete, nil)
	}
	sql := rewrite(raw)
	s.prepared[name] = &prepared{sql: sql, raw: raw, numParams: countParams(sql)}
	return s.c.send(msgParseComplete, nil)
}

func (s *session) handleBind(body []byte) error {
	r := newReader(body)
	portalName := r.cstring()
	stmtName := r.cstring()

	nFmt := r.int16()
	fmts := make([]int, nFmt)
	for i := range fmts {
		fmts[i] = r.int16()
	}

	nParams := r.int16()
	params := make([]core.Value, nParams)
	for i := 0; i < nParams; i++ {
		length := r.int32()
		if length == -1 {
			params[i] = nil
			continue
		}
		params[i] = decodeParam(formatFor(fmts, i), r.bytes(length))
	}

	nResFmt := r.int16()
	resFmts := make([]int, nResFmt)
	for i := range resFmts {
		resFmts[i] = r.int16()
	}
	if r.err != nil {
		return s.protoError("08P01", "malformed Bind message")
	}

	prep := s.prepared[stmtName]
	if prep == nil {
		return s.protoError("26000", "unknown prepared statement "+quoteName(stmtName))
	}
	s.portals[portalName] = &portal{prep: prep, params: params, formats: resFmts}
	return s.c.send(msgBindComplete, nil)
}

func (s *session) handleDescribe(body []byte) error {
	r := newReader(body)
	kind := r.byte()
	name := r.cstring()
	if r.err != nil {
		return s.protoError("08P01", "malformed Describe message")
	}

	var prep *prepared
	var args []core.Value

	switch kind {
	case 'S':
		prep = s.prepared[name]
		if prep == nil {
			return s.protoError("26000", "unknown prepared statement "+quoteName(name))
		}
		if prep.util != nil || prep.txControl != "" || prep.seqDDL != "" || prep.typeDDL != "" || prep.setRole != "" || prep.grant != "" || prep.rlsDDL != "" || prep.alterDDL != "" || prep.listenNotify != "" {
			if err := s.c.sendParameterDescription(0); err != nil {
				return err
			}
			return s.sendUtilDescribe(prep.util)
		}
		if err := s.c.sendParameterDescription(prep.numParams); err != nil {
			return err
		}
		// Probe with 0 (not NULL) so expression columns like "$1 * 2" keep a
		// concrete type we can advertise; NULL would erase it.
		args = probeArgs(prep.numParams)
	case 'P':
		pt := s.portals[name]
		if pt == nil {
			return s.protoError("34000", "unknown portal "+quoteName(name))
		}
		prep = pt.prep
		if prep.util != nil || prep.txControl != "" || prep.seqDDL != "" || prep.typeDDL != "" || prep.setRole != "" || prep.grant != "" || prep.rlsDDL != "" || prep.alterDDL != "" || prep.listenNotify != "" {
			return s.sendUtilDescribe(prep.util)
		}
		args = pt.params
	default:
		return s.protoError("08P01", "invalid Describe target")
	}

	descSQL := prep.sql
	if hasSeqFunc(prep.raw) {
		// Describe must parse for column types but never advance a sequence;
		// neutralize the calls to a constant, then rewrite the safe string.
		descSQL = rewrite(neutralizeSequences(prep.raw))
	}
	cols, err := s.describeSQL(descSQL, args)
	if err != nil {
		logQueryError("describe", err, prep.sql, prep.sql)
		return s.protoError("42000", err.Error())
	}
	if cols == nil {
		return s.c.send(msgNoData, nil)
	}
	oids := make([]uint32, len(cols))
	for i, col := range cols {
		oids[i] = oidForColumn(col, nil, i)
	}
	return s.c.sendRowDescription(cols, oids)
}

func (s *session) handleExecute(body []byte) error {
	r := newReader(body)
	portalName := r.cstring()
	maxRows := r.int32() // 0 = no limit
	if r.err != nil {
		return s.protoError("08P01", "malformed Execute message")
	}

	pt := s.portals[portalName]
	if pt == nil {
		return s.protoError("34000", "unknown portal "+quoteName(portalName))
	}

	if kind := pt.prep.txControl; kind != "" {
		tag, err := s.applyTxControl(kind)
		if err != nil {
			s.txFailed = s.tx != nil
			return s.protoError("25000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if handled, tag, code, err := s.trySavepoint(pt.prep.raw); handled {
		if err != nil {
			return s.protoError(code, err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if s.abortedTxError() {
		return s.protoError("25P02",
			"current transaction is aborted, commands ignored until end of transaction block")
	}

	if pt.prep.seqDDL != "" {
		tag, _, err := s.trySequenceDDL(pt.prep.seqDDL)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if pt.prep.typeDDL != "" {
		tag, _, err := s.tryTypeDDL(pt.prep.typeDDL)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if pt.prep.setRole != "" {
		if err := s.applySetRole(pt.prep.setRole); err != nil {
			return s.protoError("22023", err.Error())
		}
		return s.c.sendCommandComplete(firstWordUpper(pt.prep.setRole))
	}

	if pt.prep.grant != "" {
		tag, err := s.applyGrant(pt.prep.grant)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if pt.prep.rlsDDL != "" {
		tag, _, err := s.tryRLSDDL(pt.prep.rlsDDL)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42000", err.Error())
		}
		return s.c.sendCommandComplete(tag)
	}

	if pt.prep.alterDDL != "" {
		tag, _, err := s.tryAlterTable(pt.prep.alterDDL)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoExecError(err)
		}
		return s.c.sendCommandComplete(tag)
	}

	if pt.prep.listenNotify != "" {
		tag, _ := s.tryListenNotify(pt.prep.listenNotify)
		return s.c.sendCommandComplete(tag)
	}

	if u := pt.prep.util; u != nil {
		if u.IsQuery {
			oids := make([]uint32, len(u.Columns))
			for i, col := range u.Columns {
				oids[i] = oidForColumn(col, u.Rows, i)
			}
			if err := s.c.sendDataRows(u.Rows, oids, pt.formats); err != nil {
				return err
			}
		}
		return s.c.sendCommandComplete(commandTag(u))
	}

	// Run the query once; successive Executes on the same portal page through
	// the buffered rows (partial fetch).
	if pt.result == nil {
		if err := s.checkPrivileges(pt.prep.raw); err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42501", err.Error())
		}
		raw := s.applyColumnDefaults(pt.prep.raw)
		if handled, rs, err := s.tryRLSInsert(raw, pt.params); handled {
			if err != nil {
				if s.tx != nil {
					s.txFailed = true
				}
				return s.protoError("42501", err.Error())
			}
			if rs.IsQuery { // DO UPDATE … RETURNING passed the row check
				oids := make([]uint32, len(rs.Columns))
				for i, col := range rs.Columns {
					oids[i] = oidForColumn(col, rs.Rows, i)
				}
				if err := s.c.sendDataRows(rs.Rows, oids, pt.formats); err != nil {
					return err
				}
			}
			s.resetPortal(pt)
			return s.c.sendCommandComplete(commandTag(rs))
		}
		execSQL, err := s.rewriteForExec(raw)
		if err != nil {
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoError("42P01", err.Error())
		}
		rs, err := s.exec(execSQL, pt.params)
		if err != nil {
			logQueryError("execute", err, execSQL, pt.prep.sql)
			if s.tx != nil {
				s.txFailed = true
			}
			return s.protoExecError(err)
		}
		s.recordOwnership(pt.prep.raw)
		pt.result, pt.pos = rs, 0
	}

	rs := pt.result
	if !rs.IsQuery {
		s.resetPortal(pt)
		return s.c.sendCommandComplete(commandTag(rs))
	}

	// Extended protocol: DataRows only (the RowDescription went out at Describe).
	oids := make([]uint32, len(rs.Columns))
	for i, col := range rs.Columns {
		oids[i] = oidForColumn(col, rs.Rows, i)
	}
	end := len(rs.Rows)
	if maxRows > 0 && pt.pos+maxRows < end {
		end = pt.pos + maxRows
	}
	if err := s.c.sendDataRows(rs.Rows[pt.pos:end], oids, pt.formats); err != nil {
		return err
	}
	pt.pos = end
	if pt.pos < len(rs.Rows) {
		return s.c.send(msgPortalSuspended, nil) // more rows await another Execute
	}
	tag := commandTag(rs)
	s.resetPortal(pt)
	return s.c.sendCommandComplete(tag)
}

// resetPortal clears a portal's buffered result so a later Execute re-runs it.
func (s *session) resetPortal(pt *portal) {
	pt.result, pt.pos = nil, 0
}

func (s *session) handleClose(body []byte) error {
	r := newReader(body)
	kind := r.byte()
	name := r.cstring()
	if r.err != nil {
		return s.protoError("08P01", "malformed Close message")
	}
	switch kind {
	case 'S':
		delete(s.prepared, name)
	case 'P':
		delete(s.portals, name)
	}
	return s.c.send(msgCloseComplete, nil)
}

// --- helpers -----------------------------------------------------------------

// sendUtilDescribe replies to a Describe for an intercepted utility statement:
// a RowDescription for SHOW, NoData for SET/BEGIN/etc.
func (s *session) sendUtilDescribe(rs *core.ResultSet) error {
	if rs == nil || !rs.IsQuery {
		return s.c.send(msgNoData, nil)
	}
	oids := make([]uint32, len(rs.Columns))
	for i, col := range rs.Columns {
		oids[i] = oidForColumn(col, rs.Rows, i)
	}
	return s.c.sendRowDescription(rs.Columns, oids)
}

// probeArgs returns n non-NULL placeholder values used only for type
// introspection during statement Describe.
func probeArgs(n int) []core.Value {
	args := make([]core.Value, n)
	for i := range args {
		args[i] = int64(0)
	}
	return args
}

func trimStatement(sql string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(sql), ";"))
}

func quoteName(s string) string {
	if s == "" {
		return `""`
	}
	return `"` + s + `"`
}

// formatFor resolves the format code for parameter i: 0 codes means all text,
// 1 code applies to every parameter, otherwise it is per-parameter.
func formatFor(fmts []int, i int) int {
	switch len(fmts) {
	case 0:
		return 0
	case 1:
		return fmts[0]
	default:
		if i < len(fmts) {
			return fmts[i]
		}
		return 0
	}
}

// decodeParam turns a bound parameter into a core.Value. Text is passed through
// as a string (SQLite coerces as needed). Binary is a best-effort fallback,
// since we advertise unknown parameter types to steer clients toward text.
func decodeParam(format int, raw []byte) core.Value {
	if format == 0 {
		return string(raw)
	}
	switch len(raw) {
	case 8:
		return int64(byteOrder.Uint64(raw))
	case 4:
		return int64(int32(byteOrder.Uint32(raw)))
	case 2:
		return int64(int16(byteOrder.Uint16(raw)))
	case 1:
		return raw[0] != 0
	default:
		return raw
	}
}

// countParams counts positional placeholders: the highest $N index, or the
// number of bare "?" placeholders, whichever is greater.
func countParams(sql string) int {
	max, bare := 0, 0
	for i := 0; i < len(sql); i++ {
		switch sql[i] {
		case '?':
			bare++
		case '$':
			j, n := i+1, 0
			for j < len(sql) && sql[j] >= '0' && sql[j] <= '9' {
				n = n*10 + int(sql[j]-'0')
				j++
			}
			if j > i+1 && n > max {
				max = n
			}
			i = j - 1
		}
	}
	if bare > max {
		return bare
	}
	return max
}

// bodyReader is a cursor over a message body. On any short read it records an
// error and returns zero values, so callers check err once at the end.
type bodyReader struct {
	b   []byte
	pos int
	err error
}

func newReader(b []byte) *bodyReader { return &bodyReader{b: b} }

func (r *bodyReader) byte() byte {
	if r.pos+1 > len(r.b) {
		r.fail()
		return 0
	}
	v := r.b[r.pos]
	r.pos++
	return v
}

func (r *bodyReader) int16() int {
	if r.pos+2 > len(r.b) {
		r.fail()
		return 0
	}
	v := int(int16(byteOrder.Uint16(r.b[r.pos:])))
	r.pos += 2
	return v
}

func (r *bodyReader) int32() int {
	if r.pos+4 > len(r.b) {
		r.fail()
		return 0
	}
	v := int(int32(byteOrder.Uint32(r.b[r.pos:])))
	r.pos += 4
	return v
}

func (r *bodyReader) cstring() string {
	i := bytes.IndexByte(r.b[r.pos:], 0)
	if i < 0 {
		r.fail()
		return ""
	}
	s := string(r.b[r.pos : r.pos+i])
	r.pos += i + 1
	return s
}

func (r *bodyReader) bytes(n int) []byte {
	if n < 0 || r.pos+n > len(r.b) {
		r.fail()
		return nil
	}
	v := r.b[r.pos : r.pos+n]
	r.pos += n
	return v
}

func (r *bodyReader) fail() {
	if r.err == nil {
		r.err = io.ErrUnexpectedEOF
	}
}
