package postgres

import (
	"regexp"
	"strconv"
	"strings"
)

/*
Parameter types, for Describe: Postgres says what each $n is, and clients
encode their values by it. SQLite knows no such thing, so every parameter
went out unspecified, and a client sent a boolean as the text "t" — stored as
that string, not SQLite's 1, never equal to true again.

A parameter put straight into a column — INSERT's VALUES, `col = $n` in an
UPDATE, a DELETE or a one-table SELECT, `col + $n`, `col = ANY($n)` — takes
that column's type, from the table's declaration. A catalog query's
parameters take the type of the catalog column they meet: `name` for
nspname and its kin, `oid` for oids. Types a client declared in Parse are
kept, as Postgres keeps them.

Every type advertised here is one decodeTypedParam reads back in binary:
clients like tokio-postgres (Prisma's schema engine, sqlx, Diesel) send every
parameter in binary by the type they are told. They cannot send one of
unspecified type at all — tokio-postgres looks the type up in pg_type, whose
own parameter came back unspecified too, and recursed until it crashed.
Anything else is still left unspecified, which steers clients that can to
text, as before.
*/

const table = `((?:"[^"]+"|\w+)(?:\s*\.\s*(?:"[^"]+"|\w+))?)`

var (
	insertInto   = regexp.MustCompile(`(?is)^\s*INSERT\s+INTO\s+` + table + `\s*\(([^)]*)\)\s*VALUES\s*`)
	updateTable  = regexp.MustCompile(`(?is)^\s*UPDATE\s+(?:ONLY\s+)?` + table + `\s+(?:AS\s+\w+\s+)?SET\s`)
	deleteFrom   = regexp.MustCompile(`(?is)^\s*DELETE\s+FROM\s+(?:ONLY\s+)?` + table + `(?:\s+(?:AS\s+)?\w+)?\s+WHERE\s`)
	selectFrom   = regexp.MustCompile(`(?is)^\s*SELECT\s.*?\sFROM\s+` + table + `(?:\s+(?:AS\s+)?\w+)?\s+WHERE\s`)
	compared     = regexp.MustCompile(`(?i)(?:\w+\s*\.\s*)?("[^"]+"|\b\w+)\s*(?:=|<>|!=|<=|>=|<|>|\+|-|\*|/)\s*\$(\d+)\b`)
	comparedAny  = regexp.MustCompile(`(?i)(?:\w+\s*\.\s*)?("[^"]+"|\b\w+)\s*=\s*ANY\s*\(\s*\$(\d+)\s*\)`)
	placeholder  = regexp.MustCompile(`^\$(\d+)$`)
	integerDecl  = regexp.MustCompile(`(?i)^\s*(?:(?:small|big)?int(?:eger|2|4|8)?|(?:small|big)?serial[248]?)\s*$`)
	readsCatalog = regexp.MustCompile(`(?i)\b(?:pg_\w+|information_schema)\b`)
)

// Postgres' own types for the identifier and oid columns of its catalogs.
const (
	oidName    = 19
	oidOID     = 26
	oidNameArr = 1003
	oidOIDArr  = 1028
)

// catalogParamOIDs is the type of the catalog columns a client compares a
// parameter with. They only count in a statement that reads the catalog, where
// these names cannot be a table's own column.
var catalogParamOIDs = map[string]uint32{
	"nspname": oidName, "relname": oidName, "typname": oidName, "attname": oidName,
	"conname": oidName, "proname": oidName, "rolname": oidName, "datname": oidName,
	"schemaname": oidName, "tablename": oidName, "viewname": oidName, "indexname": oidName,
	"sequencename": oidName, "usename": oidName, "enumlabel": oidName,
	"table_schema": oidName, "table_name": oidName, "column_name": oidName,
	"sequence_schema": oidName, "sequence_name": oidName, "constraint_name": oidName,
	"constraint_schema": oidName, "routine_schema": oidName, "udt_schema": oidName,
	"oid": oidOID, "typelem": oidOID, "typbasetype": oidOID, "typrelid": oidOID,
	"typnamespace": oidOID, "typarray": oidOID, "relnamespace": oidOID, "reltype": oidOID,
	"pronamespace": oidOID, "connamespace": oidOID, "conrelid": oidOID, "confrelid": oidOID,
	"contypid": oidOID, "indrelid": oidOID, "indexrelid": oidOID, "attrelid": oidOID,
	"atttypid": oidOID, "enumtypid": oidOID, "objoid": oidOID, "classoid": oidOID,
	"adrelid": oidOID, "starelid": oidOID, "evtfoid": oidOID, "tgrelid": oidOID,
}

// paramTypes is the OID of each of a statement's n parameters, 0 for unknown.
// declared holds the types the client gave in Parse, which win.
func (s *session) paramTypes(sql string, n int, declared []uint32) []uint32 {
	oids := make([]uint32, n)
	if n == 0 {
		return oids
	}
	if readsCatalog.MatchString(sql) {
		for i, col := range comparedParams(sql, compared) {
			if i >= 1 && i <= n {
				oids[i-1] = catalogParamOIDs[strings.ToLower(col)]
			}
		}
		for i, col := range comparedParams(sql, comparedAny) {
			if i < 1 || i > n {
				continue
			}
			switch catalogParamOIDs[strings.ToLower(col)] {
			case oidName:
				oids[i-1] = oidNameArr
			case oidOID:
				oids[i-1] = oidOIDArr
			}
		}
	}
	if tbl, cols, anys := paramColumns(sql); tbl != "" {
		types := s.columnTypes(tbl)
		for i, col := range cols {
			if i >= 1 && i <= n && oids[i-1] == 0 {
				oids[i-1] = paramOID(types[strings.ToLower(col)])
			}
		}
		for i, col := range anys {
			if i >= 1 && i <= n && oids[i-1] == 0 {
				oids[i-1] = arrayParamOID(paramOID(types[strings.ToLower(col)]))
			}
		}
	}
	for i, oid := range declared {
		if i < n && oid != 0 {
			oids[i] = oid
		}
	}
	return oids
}

// paramOID is the type to advertise for a parameter going into a column
// declared decl: one decodeTypedParam can read in binary, or 0. Integers are
// int8, as their values are advertised.
func paramOID(decl string) uint32 {
	if decl == "" || isArrayDecl(decl) || strings.EqualFold(strings.TrimSpace(decl), "hstore") ||
		strings.Contains(strings.ToUpper(decl), "DECIMALTEXT") {
		return 0
	}
	if oid, ok := strictDeclOID(decl); ok {
		return oid
	}
	switch oid := oidFromDeclType(decl); oid {
	case oidBool, oidFloat8, oidTimestamp, oidTimestamptz, oidDate:
		return oid
	case oidInt8:
		// SQLite's affinity finds INT in an interval or a point too.
		if integerDecl.MatchString(decl) {
			return oid
		}
	case oidText:
		// Text is also where any type SQLite does not recognize lands (an
		// interval, an enum); only a declared text type is one.
		if isTextAffinity(decl) {
			return oid
		}
	}
	return 0
}

// arrayParamOID is the array of a scalar parameter type, for `= ANY($n)`.
func arrayParamOID(elem uint32) uint32 {
	switch elem {
	case oidText, oidVarchar, oidBpchar:
		return oidTextArr
	case oidInt8:
		return oidInt8Arr
	case oidUUID:
		return oidUUIDArr
	}
	return 0
}

// comparedParams is the column each parameter re matches meets, by number.
func comparedParams(sql string, re *regexp.Regexp) map[int]string {
	cols := map[int]string{}
	for _, m := range re.FindAllStringSubmatch(sql, -1) {
		n, _ := strconv.Atoi(m[2])
		cols[n] = unquote(m[1])
	}
	return cols
}

// paramColumns is the table a statement writes or reads, and the column each
// parameter goes into, by its number — or, for `col = ANY($n)`, whose type
// its elements take.
func paramColumns(sql string) (string, map[int]string, map[int]string) {
	cols := map[int]string{}
	if m := insertInto.FindStringSubmatchIndex(sql); m != nil {
		tbl := sql[m[2]:m[3]]
		names := splitTop(sql[m[4]:m[5]])
		for _, tuple := range tuples(sql[m[1]:]) {
			for i, item := range splitTop(tuple) {
				if p := placeholder.FindStringSubmatch(strings.TrimSpace(item)); p != nil && i < len(names) {
					n, _ := strconv.Atoi(p[1])
					cols[n] = unquote(names[i])
				}
			}
		}
		return tbl, cols, nil
	}
	var tbl string
	for _, re := range []*regexp.Regexp{updateTable, deleteFrom, selectFrom} {
		if m := re.FindStringSubmatch(sql); m != nil {
			tbl = m[1]
			break
		}
	}
	if tbl == "" || strings.Contains(strings.ToUpper(sql), " JOIN ") {
		return "", nil, nil
	}
	return tbl, comparedParams(sql, compared), comparedParams(sql, comparedAny)
}

// columnTypes is a table's declared column types, by lowercase name.
func (s *session) columnTypes(tbl string) map[string]string {
	parts := strings.Split(tbl, ".")
	name := unquote(parts[len(parts)-1])
	if len(parts) == 2 && !strings.EqualFold(unquote(parts[0]), "public") {
		return nil
	}
	rs, err := s.exec("SELECT name, type FROM pragma_table_info("+sqlStr(name)+")", nil)
	if err != nil || rs == nil {
		return nil
	}
	out := map[string]string{}
	for _, row := range rs.Rows {
		if len(row) == 2 {
			out[strings.ToLower(asString(row[0]))] = asString(row[1])
		}
	}
	return out
}

func unquote(name string) string {
	name = strings.TrimSpace(name)
	if len(name) >= 2 && name[0] == '"' && name[len(name)-1] == '"' {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
	}
	return name
}

// tuples is the parenthesised rows a VALUES list starts with: their insides.
func tuples(s string) []string {
	var out []string
	for {
		s = strings.TrimLeft(s, " \t\r\n,")
		if !strings.HasPrefix(s, "(") {
			return out
		}
		end := closing(s)
		if end < 0 {
			return out
		}
		out = append(out, s[1:end])
		s = s[end+1:]
	}
}

// closing is the index of the parenthesis closing the one s starts with.
func closing(s string) int {
	depth, quoted := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return -1
}

// splitTop splits on the commas outside parentheses and quotes.
func splitTop(s string) []string {
	var out []string
	depth, quoted, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\'':
			quoted = !quoted
		case quoted:
		case c == '(':
			depth++
		case c == ')':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
