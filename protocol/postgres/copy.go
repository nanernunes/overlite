package postgres

import (
	"encoding/csv"
	"fmt"
	"regexp"
	"strings"

	"overlite/core"
)

// COPY implements the bulk-data sub-protocol used by pg_dump and psql's \copy.
// Supported: COPY <table> [(cols)] FROM STDIN, COPY <table> [(cols)] TO STDOUT,
// and COPY (query) TO STDOUT, in text (default) and CSV formats.

type copyStmt struct {
	table     string
	columns   []string // empty = all columns
	query     string   // set for COPY (query) TO STDOUT
	fromStdin bool     // FROM STDIN (true) vs TO STDOUT (false)
	csv       bool
	delimiter byte
	null      string
	header    bool
}

var reCopy = regexp.MustCompile(`(?is)^\s*COPY\s+(.*?)\s+(FROM\s+STDIN|TO\s+STDOUT)\s*(.*)$`)

// parseCopy parses a COPY statement, or returns ok=false if it isn't one.
func parseCopy(sql string) (*copyStmt, bool) {
	m := reCopy.FindStringSubmatch(sql)
	if m == nil {
		return nil, false
	}
	cs := &copyStmt{
		fromStdin: strings.EqualFold(strings.Fields(m[2])[0], "from"),
		delimiter: '\t',
		null:      `\N`,
	}

	target := strings.TrimSpace(m[1])
	if strings.HasPrefix(target, "(") {
		cs.query = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(target, "("), ")"))
	} else if i := strings.IndexByte(target, '('); i >= 0 {
		cs.table = strings.TrimSpace(target[:i])
		cols := strings.TrimSuffix(strings.TrimSpace(target[i+1:]), ")")
		for _, c := range strings.Split(cols, ",") {
			cs.columns = append(cs.columns, unquoteIdent(strings.TrimSpace(c)))
		}
	} else {
		cs.table = target
	}

	parseCopyOptions(cs, m[3])
	return cs, true
}

func parseCopyOptions(cs *copyStmt, opts string) {
	low := strings.ToLower(opts)
	if strings.Contains(low, "csv") || regexpContains(low, `format\s+csv`) {
		cs.csv = true
		cs.delimiter = ','
		cs.null = ""
	}
	if strings.Contains(low, "header") {
		cs.header = true
	}
	if m := regexp.MustCompile(`(?i)delimiter\s+'(.)'`).FindStringSubmatch(opts); m != nil {
		cs.delimiter = m[1][0]
	}
	if m := regexp.MustCompile(`(?i)null\s+'([^']*)'`).FindStringSubmatch(opts); m != nil {
		cs.null = m[1]
	}
}

func regexpContains(s, pat string) bool {
	return regexp.MustCompile(pat).MatchString(s)
}

// handleCopy runs a COPY statement, driving the copy sub-protocol on the wire.
func (s *session) handleCopy(cs *copyStmt) error {
	if cs.fromStdin {
		return s.copyIn(cs)
	}
	return s.copyOut(cs)
}

// --- COPY ... TO STDOUT -------------------------------------------------------

func (s *session) copyOut(cs *copyStmt) error {
	query := cs.query
	if query == "" {
		cols := "*"
		if len(cs.columns) > 0 {
			cols = quoteCols(cs.columns)
		}
		query = "SELECT " + cols + " FROM " + cs.table
	}
	rs, err := s.exec(rewrite(query), nil)
	if err != nil {
		return s.c.sendError("42000", err.Error())
	}

	// CopyOutResponse: text format (0), one format code per column.
	if err := s.c.sendCopyResponse(msgCopyOutResponse, len(rs.Columns)); err != nil {
		return err
	}

	w := newCopyWriter(cs)
	if cs.header {
		names := make([]string, len(rs.Columns))
		for i, c := range rs.Columns {
			names[i] = c.Name
		}
		if err := s.c.send(msgCopyData, w.line(names)); err != nil {
			return err
		}
	}
	for _, row := range rs.Rows {
		if err := s.c.send(msgCopyData, w.row(row)); err != nil {
			return err
		}
	}
	if err := s.c.send(msgCopyDone, nil); err != nil {
		return err
	}
	return s.c.sendCommandComplete(fmt.Sprintf("COPY %d", len(rs.Rows)))
}

// --- COPY ... FROM STDIN ------------------------------------------------------

func (s *session) copyIn(cs *copyStmt) error {
	cols := cs.columns
	if len(cols) == 0 {
		describe, err := s.db.Describe(s.ctx, "SELECT * FROM "+rewrite(cs.table), nil)
		if err != nil {
			return s.c.sendError("42000", err.Error())
		}
		for _, c := range describe {
			cols = append(cols, c.Name)
		}
	}

	if err := s.c.sendCopyResponse(msgCopyInResponse, len(cols)); err != nil {
		return err
	}
	if err := s.c.flush(); err != nil {
		return err
	}

	// Accumulate all CopyData until CopyDone (or abort on CopyFail).
	var data []byte
	for {
		typ, body, err := s.c.readMessage()
		if err != nil {
			return err
		}
		switch typ {
		case msgCopyData:
			data = append(data, body...)
		case msgCopyDone:
			n, err := s.copyInsert(cs, cols, data)
			if err != nil {
				if s.tx != nil {
					s.txFailed = true
				}
				return s.c.sendError("42000", err.Error())
			}
			return s.c.sendCommandComplete(fmt.Sprintf("COPY %d", n))
		case msgCopyFail:
			return s.c.sendError("57014", "COPY from stdin failed: "+strings.TrimRight(string(body), "\x00"))
		default:
			return s.c.sendError("08P01", "unexpected message during COPY")
		}
	}
}

// copyInsert parses the received data and inserts it, atomically when not
// already inside a transaction.
func (s *session) copyInsert(cs *copyStmt, cols []string, data []byte) (int, error) {
	rows, err := parseCopyData(cs, len(cols), data)
	if err != nil {
		return 0, err
	}

	insert := "INSERT INTO " + rewrite(cs.table) + " (" + quoteCols(cols) + ") VALUES (" +
		placeholders(len(cols)) + ")"

	// Wrap in a transaction so a bad row doesn't leave a partial load.
	exec := s.exec
	ownTx := s.tx == nil
	var tx core.Tx
	if ownTx {
		if tx, err = s.db.Begin(s.ctx); err != nil {
			return 0, err
		}
		exec = func(sql string, args []core.Value) (*core.ResultSet, error) {
			return tx.Execute(s.ctx, sql, args)
		}
	}

	for _, row := range rows {
		if _, err := exec(insert, row); err != nil {
			if ownTx {
				tx.Rollback()
			}
			return 0, err
		}
	}
	if ownTx {
		if err := tx.Commit(); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// --- wire helpers -------------------------------------------------------------

func (c *wireConn) sendCopyResponse(typ byte, numCols int) error {
	var b []byte
	b = append(b, 0) // overall format: 0 = text
	b = appendInt16(b, numCols)
	for i := 0; i < numCols; i++ {
		b = appendInt16(b, 0) // per-column format: text
	}
	return c.send(typ, b)
}

func quoteCols(cols []string) string {
	q := make([]string, len(cols))
	for i, c := range cols {
		q[i] = `"` + c + `"`
	}
	return strings.Join(q, ", ")
}

func placeholders(n int) string {
	p := make([]string, n)
	for i := range p {
		p[i] = "?"
	}
	return strings.Join(p, ", ")
}

// --- text/CSV encoding --------------------------------------------------------

type copyWriter struct{ cs *copyStmt }

func newCopyWriter(cs *copyStmt) *copyWriter { return &copyWriter{cs: cs} }

// row encodes one result row as a copy line (with trailing newline).
func (w *copyWriter) row(cells []core.Value) []byte {
	fields := make([]string, len(cells))
	for i, cell := range cells {
		if cell == nil {
			fields[i] = w.cs.null
			continue
		}
		fields[i] = valueToText(cell)
	}
	return w.line(fields)
}

func (w *copyWriter) line(fields []string) []byte {
	if w.cs.csv {
		return []byte(csvLine(fields, rune(w.cs.delimiter)))
	}
	esc := make([]string, len(fields))
	for i, f := range fields {
		esc[i] = escapeCopyText(f)
	}
	return []byte(strings.Join(esc, string(w.cs.delimiter)) + "\n")
}

func csvLine(fields []string, delim rune) string {
	var sb strings.Builder
	wr := csv.NewWriter(&sb)
	wr.Comma = delim
	wr.Write(fields)
	wr.Flush()
	return sb.String() // includes trailing newline
}

func escapeCopyText(s string) string {
	return strings.NewReplacer(
		"\\", "\\\\", "\t", "\\t", "\n", "\\n", "\r", "\\r",
	).Replace(s)
}

// parseCopyData splits received bytes into rows of values.
func parseCopyData(cs *copyStmt, numCols int, data []byte) ([][]core.Value, error) {
	text := strings.TrimRight(string(data), "\n")
	if text == "" {
		return nil, nil
	}
	if cs.csv {
		return parseCSV(cs, numCols, text)
	}
	var rows [][]core.Value
	for i, line := range strings.Split(text, "\n") {
		// A lone \. ends the data. pg_dump writes one after every COPY block
		// and psql forwards it as ordinary CopyData, so without this the
		// marker is read as a one-field row and the whole load fails.
		if line == `\.` {
			break
		}
		fields := strings.Split(line, string(cs.delimiter))
		if len(fields) != numCols {
			return nil, fmt.Errorf("COPY row %d has %d fields, expected %d", i+1, len(fields), numCols)
		}
		row := make([]core.Value, numCols)
		for j, f := range fields {
			if f == cs.null {
				row[j] = nil
			} else {
				row[j] = unescapeCopyText(f)
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func parseCSV(cs *copyStmt, numCols int, text string) ([][]core.Value, error) {
	r := csv.NewReader(strings.NewReader(text))
	r.Comma = rune(cs.delimiter)
	r.FieldsPerRecord = numCols
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	var rows [][]core.Value
	for i, rec := range records {
		if cs.header && i == 0 {
			continue
		}
		row := make([]core.Value, numCols)
		for j, f := range rec {
			if f == cs.null {
				row[j] = nil
			} else {
				row[j] = f
			}
		}
		rows = append(rows, row)
	}
	return rows, nil
}

func unescapeCopyText(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			switch s[i+1] {
			case 't':
				b.WriteByte('\t')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case '\\':
				b.WriteByte('\\')
			default:
				b.WriteByte(s[i+1])
			}
			i++
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// valueToText renders a scalar for the copy text stream.
func valueToText(v core.Value) string {
	switch val := v.(type) {
	case []byte:
		return string(val)
	case bool:
		if val {
			return "t"
		}
		return "f"
	default:
		return fmt.Sprint(val)
	}
}
