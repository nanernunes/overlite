package postgres

import (
	"fmt"
	"strconv"
	"strings"
)

// Sequences: CREATE/ALTER/DROP SEQUENCE are intercepted here and translated to
// the engine's internal _overlite_sequences table; nextval/currval/setval/
// lastval calls are expanded to concrete integer literals before the SQL runs,
// so SQLite only ever stores ordinary integers (the file stays usable without
// overlite). currval/lastval are session-scoped, tracked on the session.

// trySequenceDDL handles CREATE/ALTER/DROP SEQUENCE. It returns handled=true
// when it recognized the statement (mirrors tryRoleDDL / trySchemaDDL).
func (s *session) trySequenceDDL(sql string) (tag string, handled bool, err error) {
	fields := strings.Fields(sql)
	if len(fields) < 2 || !strings.EqualFold(fields[1], "sequence") {
		return "", false, nil
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE":
		return "CREATE SEQUENCE", true, s.createSequence(fields[2:])
	case "ALTER":
		return "ALTER SEQUENCE", true, s.alterSequence(fields[2:])
	case "DROP":
		return "DROP SEQUENCE", true, s.dropSequence(fields[2:])
	}
	return "", false, nil
}

// isSequenceDDL reports whether sql is a CREATE/ALTER/DROP SEQUENCE statement
// (used by the extended-query path to route it like other utility DDL).
func isSequenceDDL(sql string) bool {
	fields := strings.Fields(sql)
	if len(fields) < 2 || !strings.EqualFold(fields[1], "sequence") {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CREATE", "ALTER", "DROP":
		return true
	}
	return false
}

// seqOptions are the CREATE/ALTER SEQUENCE settings we track.
type seqOptions struct {
	start     int64
	increment int64
	minValue  int64
	maxValue  int64
	cache     int64
	cycle     bool

	startSet, minSet, maxSet bool
}

func defaultSeqOptions() seqOptions {
	return seqOptions{start: 1, increment: 1, minValue: 1, maxValue: 9223372036854775807, cache: 1}
}

func (s *session) createSequence(rest []string) error {
	ifNotExists := false
	if len(rest) >= 3 && strings.EqualFold(rest[0], "if") &&
		strings.EqualFold(rest[1], "not") && strings.EqualFold(rest[2], "exists") {
		ifNotExists = true
		rest = rest[3:]
	}
	if len(rest) == 0 {
		return fmt.Errorf("syntax error in CREATE SEQUENCE")
	}
	name := seqRef(rest[0])
	opts := defaultSeqOptions()
	if err := parseSeqOptions(rest[1:], &opts); err != nil {
		return err
	}
	// A descending sequence with no explicit MINVALUE defaults to a very low min.
	if opts.increment < 0 && !opts.minSet {
		opts.minValue = -9223372036854775808
	}
	if opts.increment < 0 && !opts.maxSet {
		opts.maxValue = -1
	}
	if opts.increment < 0 && !opts.startSet {
		opts.start = opts.maxValue
	}

	verb := "INSERT"
	if ifNotExists {
		verb = "INSERT OR IGNORE"
	}
	sql := verb + " INTO _overlite_sequences" +
		" (seqname, last_value, increment, min_value, max_value, start_value, cache_size, is_cycled, is_called)" +
		" VALUES (" + sqlStr(name) + ", " +
		i64(opts.start) + ", " + i64(opts.increment) + ", " +
		i64(opts.minValue) + ", " + i64(opts.maxValue) + ", " +
		i64(opts.start) + ", " + i64(opts.cache) + ", " + b2i(opts.cycle) + ", 0)"
	_, err := s.exec(sql, nil)
	return err
}

func (s *session) alterSequence(rest []string) error {
	if len(rest) == 0 {
		return fmt.Errorf("syntax error in ALTER SEQUENCE")
	}
	name := seqRef(rest[0])
	rest = rest[1:]

	// ALTER SEQUENCE x OWNED BY / OWNER TO ... — ownership, accepted as a no-op
	// (pg_dump emits these for serial-owned sequences).
	if len(rest) >= 1 && (strings.EqualFold(rest[0], "owned") || strings.EqualFold(rest[0], "owner")) {
		return nil
	}

	// ALTER SEQUENCE x RESTART [WITH n] resets the counter (start unless given).
	if len(rest) >= 1 && strings.EqualFold(rest[0], "restart") {
		var val string
		if len(rest) >= 2 && strings.EqualFold(rest[1], "with") && len(rest) >= 3 {
			val = rest[2]
		} else if len(rest) >= 2 {
			val = rest[1]
		}
		if n, ok := parseInt(val); ok {
			_, err := s.exec("UPDATE _overlite_sequences SET last_value = "+i64(n)+
				", is_called = 0 WHERE seqname = "+sqlStr(name), nil)
			return err
		}
		_, err := s.exec("UPDATE _overlite_sequences SET last_value = start_value,"+
			" is_called = 0 WHERE seqname = "+sqlStr(name), nil)
		return err
	}

	// Otherwise apply whatever options were named (INCREMENT/MIN/MAX/CYCLE).
	var opts seqOptions
	if err := parseSeqOptions(rest, &opts); err != nil {
		return err
	}
	set := []string{}
	if opts.increment != 0 {
		set = append(set, "increment = "+i64(opts.increment))
	}
	if opts.minSet {
		set = append(set, "min_value = "+i64(opts.minValue))
	}
	if opts.maxSet {
		set = append(set, "max_value = "+i64(opts.maxValue))
	}
	set = append(set, "is_cycled = "+b2i(opts.cycle))
	if len(set) == 0 {
		return nil
	}
	_, err := s.exec("UPDATE _overlite_sequences SET "+strings.Join(set, ", ")+
		" WHERE seqname = "+sqlStr(name), nil)
	return err
}

func (s *session) dropSequence(rest []string) error {
	if len(rest) >= 2 && strings.EqualFold(rest[0], "if") && strings.EqualFold(rest[1], "exists") {
		rest = rest[2:]
	}
	for _, name := range strings.Split(strings.Join(rest, " "), ",") {
		name = seqRef(strings.TrimSpace(strings.TrimRight(name, ";")))
		if name == "" || strings.EqualFold(name, "cascade") || strings.EqualFold(name, "restrict") {
			continue
		}
		if _, err := s.exec("DELETE FROM _overlite_sequences WHERE seqname = "+sqlStr(name), nil); err != nil {
			return err
		}
		delete(s.seqCurr, strings.ToLower(name))
	}
	return nil
}

// parseSeqOptions reads the option keywords shared by CREATE and ALTER SEQUENCE.
func parseSeqOptions(tokens []string, o *seqOptions) error {
	for i := 0; i < len(tokens); i++ {
		switch strings.ToUpper(strings.TrimRight(tokens[i], ";")) {
		case "START":
			if i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "with") {
				i++
			}
			if n, ok := nextInt(tokens, &i); ok {
				o.start, o.startSet = n, true
			}
		case "INCREMENT":
			if i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "by") {
				i++
			}
			if n, ok := nextInt(tokens, &i); ok {
				o.increment = n
			}
		case "MINVALUE":
			if n, ok := nextInt(tokens, &i); ok {
				o.minValue, o.minSet = n, true
			}
		case "MAXVALUE":
			if n, ok := nextInt(tokens, &i); ok {
				o.maxValue, o.maxSet = n, true
			}
		case "CACHE":
			if n, ok := nextInt(tokens, &i); ok {
				o.cache = n
			}
		case "CYCLE":
			o.cycle = true
		case "NO":
			// NO MINVALUE / NO MAXVALUE / NO CYCLE — keep defaults / clear cycle.
			if i+1 < len(tokens) && strings.EqualFold(tokens[i+1], "cycle") {
				o.cycle = false
			}
			i++
		case "AS", "OWNED":
			i++ // AS <type> / OWNED BY <col> — accepted, ignored
		}
	}
	return nil
}

// nextInt reads the integer token following index *i, advancing *i past it.
func nextInt(tokens []string, i *int) (int64, bool) {
	if *i+1 >= len(tokens) {
		return 0, false
	}
	if n, ok := parseInt(tokens[*i+1]); ok {
		*i++
		return n, true
	}
	return 0, false
}

// --- nextval/currval/setval/lastval expansion --------------------------------

// expandSequences replaces every nextval/currval/setval/lastval call in sql with
// the integer literal it evaluates to (advancing sequences as a side effect). It
// is skipped for DDL so a nextval() never lands in a stored schema/default, which
// would break the file for a plain SQLite user.
func (s *session) expandSequences(sql string) (string, error) {
	if !hasSeqFunc(sql) {
		return sql, nil
	}
	switch firstWordUpper(sql) {
	case "CREATE", "ALTER", "DROP", "TRUNCATE":
		return sql, nil
	}
	return scanSeqCalls(sql, s.evalSeqFunc)
}

// neutralizeSequences rewrites sequence calls to a constant 0 without advancing
// anything. Used for Describe, where SQLite must parse the statement for its
// column types but must not execute the sequence side effects.
func neutralizeSequences(sql string) string {
	if !hasSeqFunc(sql) {
		return sql
	}
	out, _ := scanSeqCalls(sql, func(string, string) (string, error) { return "0", nil })
	return out
}

// scanSeqCalls walks sql and replaces each top-level nextval/currval/setval/
// lastval call with replace(fn, args), respecting single-quoted strings so it
// never rewrites text that merely contains the word.
func scanSeqCalls(sql string, replace func(fn, args string) (string, error)) (string, error) {
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			j := skipString(sql, i)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if isIdentStart(c) {
			j := i
			for j < len(sql) && isIdentPart(sql[j]) {
				j++
			}
			word := sql[i:j]
			fn := strings.ToLower(word)
			k := j
			for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t' || sql[k] == '\n' || sql[k] == '\r') {
				k++
			}
			if isSeqFunc(fn) && k < len(sql) && sql[k] == '(' {
				args, end, ok := readParenArgs(sql, k)
				if ok {
					lit, err := replace(fn, args)
					if err != nil {
						return "", err
					}
					b.WriteString(lit)
					i = end
					continue
				}
			}
			b.WriteString(word)
			i = j
			continue
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), nil
}

func (s *session) evalSeqFunc(fn, args string) (string, error) {
	switch fn {
	case "nextval":
		return s.nextval(seqRef(args))
	case "currval":
		return s.currval(seqRef(args))
	case "lastval":
		return s.lastval()
	case "setval":
		return s.setval(args)
	}
	return "", fmt.Errorf("unknown sequence function %q", fn)
}

func (s *session) nextval(name string) (string, error) {
	rs, err := s.exec("SELECT last_value, increment, min_value, max_value, is_cycled, is_called"+
		" FROM _overlite_sequences WHERE seqname = "+sqlStr(name), nil)
	if err != nil {
		return "", err
	}
	if len(rs.Rows) == 0 {
		return "", fmt.Errorf("relation %q does not exist", name)
	}
	row := rs.Rows[0]
	last := toInt64(row[0])
	incr := toInt64(row[1])
	minV := toInt64(row[2])
	maxV := toInt64(row[3])
	cycled := toInt64(row[4]) != 0
	called := toInt64(row[5]) != 0

	next := last
	if called {
		next = last + incr
		if incr > 0 && next > maxV {
			if !cycled {
				return "", fmt.Errorf("nextval: reached maximum value of sequence %q (%d)", name, maxV)
			}
			next = minV
		} else if incr < 0 && next < minV {
			if !cycled {
				return "", fmt.Errorf("nextval: reached minimum value of sequence %q (%d)", name, minV)
			}
			next = maxV
		}
	}
	if _, err := s.exec("UPDATE _overlite_sequences SET last_value = "+i64(next)+
		", is_called = 1 WHERE seqname = "+sqlStr(name), nil); err != nil {
		return "", err
	}
	s.rememberSeq(name, next)
	return i64(next), nil
}

func (s *session) currval(name string) (string, error) {
	if v, ok := s.seqCurr[strings.ToLower(name)]; ok {
		return i64(v), nil
	}
	return "", fmt.Errorf("currval of sequence %q is not yet defined in this session", name)
}

func (s *session) lastval() (string, error) {
	if s.seqLast == "" {
		return "", fmt.Errorf("lastval is not yet defined in this session")
	}
	return i64(s.seqCurr[s.seqLast]), nil
}

func (s *session) setval(args string) (string, error) {
	parts := splitTopLevel(args)
	if len(parts) < 2 {
		return "", fmt.Errorf("setval requires (sequence, value[, is_called])")
	}
	name := seqRef(parts[0])
	val, ok := parseInt(strings.TrimSpace(parts[1]))
	if !ok {
		return "", fmt.Errorf("setval: invalid value %q", strings.TrimSpace(parts[1]))
	}
	isCalled := true
	if len(parts) >= 3 {
		isCalled = parseBool(strings.TrimSpace(parts[2]))
	}
	rs, err := s.exec("UPDATE _overlite_sequences SET last_value = "+i64(val)+", is_called = "+
		b2i(isCalled)+" WHERE seqname = "+sqlStr(name), nil)
	if err != nil {
		return "", err
	}
	if rs.RowsAffected == 0 {
		return "", fmt.Errorf("relation %q does not exist", name)
	}
	if isCalled {
		s.rememberSeq(name, val)
	}
	return i64(val), nil
}

func (s *session) rememberSeq(name string, v int64) {
	key := strings.ToLower(name)
	s.seqCurr[key] = v
	s.seqLast = key
}

// --- small parsing helpers ----------------------------------------------------

func hasSeqFunc(sql string) bool {
	l := strings.ToLower(sql)
	return strings.Contains(l, "nextval") || strings.Contains(l, "currval") ||
		strings.Contains(l, "setval") || strings.Contains(l, "lastval")
}

func isSeqFunc(fn string) bool {
	return fn == "nextval" || fn == "currval" || fn == "setval" || fn == "lastval"
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

// skipString returns the index just past the single-quoted string starting at i.
func skipString(sql string, i int) int {
	j := i + 1
	for j < len(sql) {
		if sql[j] == '\'' {
			if j+1 < len(sql) && sql[j+1] == '\'' {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return j
}

// readParenArgs reads a parenthesized argument list starting at the '(' at open,
// honoring nested parens and quoted strings. It returns the inner text, the
// index just past the ')', and ok.
func readParenArgs(sql string, open int) (string, int, bool) {
	depth, i, start := 0, open, open+1
	for i < len(sql) {
		switch sql[i] {
		case '\'':
			i = skipString(sql, i)
			continue
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return sql[start:i], i + 1, true
			}
		}
		i++
	}
	return "", 0, false
}

// seqRef extracts the sequence name from a nextval/currval argument, stripping
// quotes, a ::regclass/::text cast, and any schema qualifier.
func seqRef(arg string) string {
	a := strings.TrimSpace(arg)
	if i := strings.Index(a, "::"); i >= 0 {
		a = strings.TrimSpace(a[:i])
	}
	a = strings.TrimSpace(strings.Trim(a, "'"))
	a = strings.Trim(a, `"`)
	if i := strings.LastIndexByte(a, '.'); i >= 0 {
		a = a[i+1:]
	}
	return a
}

// splitTopLevel splits on commas that are not inside quotes or parentheses.
func splitTopLevel(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\'':
			i = skipString(s, i) - 1
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, s[start:i])
				start = i + 1
			}
		}
	}
	return append(out, s[start:])
}

func parseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimRight(s, ";")), 10, 64)
	return n, err == nil
}

func parseBool(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "false", "f", "0", "'false'", "'f'":
		return false
	default:
		return true
	}
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }
func b2i(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

func sqlStr(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
