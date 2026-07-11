package postgres

import (
	"encoding/json"
	"strings"

	"overlite/core"
)

// Postgres arrays over SQLite: a `type[]` column keeps its declared type
// verbatim (SQLite stores anything), and the value is a JSON array in a TEXT
// cell — a valid SQLite value that plain SQLite can still read. On the wire we
// advertise the proper array OID and render the JSON as Postgres' `{…}` text;
// on input, ARRAY[…] and '{…}'::type[] become JSON.

// Array type OIDs (element OID -> array OID).
const (
	oidBoolArr    = 1000
	oidByteaArr   = 1001
	oidInt2Arr    = 1005
	oidInt4Arr    = 1007
	oidTextArr    = 1009
	oidVarcharArr = 1015
	oidInt8Arr    = 1016
	oidFloat8Arr  = 1022
	oidDateArr    = 1182
	oidTimestpArr = 1115
	oidNumericArr = 1231
	oidUUIDArr    = 2951
	oidJSONArr    = 199
	oidJSONBArr   = 3807
)

var arrayOIDs = map[uint32]bool{
	oidBoolArr: true, oidByteaArr: true, oidInt2Arr: true, oidInt4Arr: true,
	oidTextArr: true, oidVarcharArr: true, oidInt8Arr: true, oidFloat8Arr: true,
	oidDateArr: true, oidTimestpArr: true, oidNumericArr: true, oidUUIDArr: true,
	oidJSONArr: true, oidJSONBArr: true,
}

func isArrayOID(oid uint32) bool { return arrayOIDs[oid] }

// isArrayDecl reports whether a declared type is a Postgres array (`type[]`).
func isArrayDecl(decl string) bool {
	return strings.HasSuffix(strings.TrimSpace(decl), "[]")
}

// arrayOIDForDecl maps a `type[]` declared type to its array OID via the element
// type's scalar OID.
func arrayOIDForDecl(decl string) uint32 {
	elem := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(decl), "[]"))
	switch oidFromDeclType(elem) {
	case oidInt8:
		return oidInt8Arr
	case oidBool:
		return oidBoolArr
	case oidFloat8:
		return oidFloat8Arr
	case oidNumeric:
		return oidNumericArr
	case oidUUID:
		return oidUUIDArr
	case oidDate:
		return oidDateArr
	case oidTimestamp:
		return oidTimestpArr
	case oidJSONB:
		return oidJSONBArr
	case oidBytea:
		return oidByteaArr
	default:
		return oidTextArr
	}
}

// jsonArrayToPGText renders a stored JSON array as Postgres' `{…}` array text.
// A value that isn't a JSON array is passed through unchanged.
func jsonArrayToPGText(v core.Value) []byte {
	s := asString(v)
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var arr []interface{}
	if err := dec.Decode(&arr); err != nil {
		return []byte(s)
	}
	return []byte(pgArrayText(arr))
}

func pgArrayText(arr []interface{}) string {
	parts := make([]string, len(arr))
	for i, e := range arr {
		parts[i] = pgArrayElem(e)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func pgArrayElem(e interface{}) string {
	switch x := e.(type) {
	case nil:
		return "NULL"
	case bool:
		if x {
			return "t"
		}
		return "f"
	case json.Number:
		return string(x)
	case string:
		return quotePGArrayElem(x)
	case []interface{}:
		return pgArrayText(x)
	default:
		return quotePGArrayElem(asString(x))
	}
}

// quotePGArrayElem double-quotes an element when it would otherwise be ambiguous
// (empty, contains a delimiter/quote/space, or looks like NULL).
func quotePGArrayElem(s string) string {
	if s == "" || strings.EqualFold(s, "null") || strings.ContainsAny(s, `{},"\ `) {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	return s
}

// --- input rewrites ---------------------------------------------------------

// rewriteArrayLiteral turns ARRAY[…] constructors into json_array(…), so the
// stored value is a JSON array. Nested ARRAY[…] are handled by the same pass.
func rewriteArrayLiteral(sql string) string {
	if !strings.Contains(strings.ToLower(sql), "array") {
		return sql
	}
	low := strings.ToLower(sql)
	var b strings.Builder
	var stack []bool // true = an ARRAY[ bracket (emit ")" on close)
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			j := endOfStringLiteral(sql, i)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if (c|0x20) == 'a' && matchWordAt(low, i, "array") {
			k := i + len("array")
			for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t' || sql[k] == '\n') {
				k++
			}
			if k < len(sql) && sql[k] == '[' {
				b.WriteString("json_array(")
				stack = append(stack, true)
				i = k + 1
				continue
			}
		}
		switch c {
		case '[':
			stack = append(stack, false)
			b.WriteByte('[')
		case ']':
			if n := len(stack); n > 0 {
				arr := stack[n-1]
				stack = stack[:n-1]
				if arr {
					b.WriteByte(')')
					i++
					continue
				}
			}
			b.WriteByte(']')
		default:
			b.WriteByte(c)
		}
		i++
	}
	return b.String()
}

// rewriteArrayCast turns '{…}'::type[] literals into a JSON-array string literal.
func rewriteArrayCast(sql string) string {
	if !strings.Contains(sql, "::") {
		return sql
	}
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		if c != '\'' {
			b.WriteByte(c)
			i++
			continue
		}
		j := endOfStringLiteral(sql, i)
		lit := sql[i:j]
		k := j
		for k < len(sql) && (sql[k] == ' ' || sql[k] == '\t') {
			k++
		}
		if k+1 < len(sql) && sql[k] == ':' && sql[k+1] == ':' {
			m := k + 2
			for m < len(sql) && (sql[m] == ' ' || sql[m] == '\t') {
				m++
			}
			ts := m
			for m < len(sql) && (isIdentPart(sql[m]) || sql[m] == '[' || sql[m] == ']' || sql[m] == '"') {
				m++
			}
			typ := strings.TrimSpace(sql[ts:m])
			if isArrayDecl(typ) && len(lit) >= 2 {
				elem := strings.TrimSuffix(typ, "[]")
				b.WriteString("'" + pgArrayLitToJSON(lit[1:len(lit)-1], elem) + "'")
				i = m
				continue
			}
		}
		b.WriteString(lit)
		i = j
	}
	return b.String()
}

// rewriteArrayCastDrop removes an `::type[]` cast from a non-literal operand: the
// value is already a JSON array, so casting to an array type is a no-op. (Literal
// operands are handled earlier by rewriteArrayCast.)
func rewriteArrayCastDrop(sql string) string {
	if !strings.Contains(sql, "::") {
		return sql
	}
	var b strings.Builder
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '\'' {
			j := endOfStringLiteral(sql, i)
			b.WriteString(sql[i:j])
			i = j
			continue
		}
		if c == ':' && i+1 < len(sql) && sql[i+1] == ':' {
			m := i + 2
			for m < len(sql) && (sql[m] == ' ' || sql[m] == '\t') {
				m++
			}
			ts := m
			for m < len(sql) && (isIdentPart(sql[m]) || sql[m] == '[' || sql[m] == ']' || sql[m] == '"') {
				m++
			}
			if isArrayDecl(strings.TrimSpace(sql[ts:m])) {
				i = m // drop the ::type[]
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

// pgArrayLitToJSON converts a Postgres array literal body ("{a,b}") into a JSON
// array, quoting elements for non-numeric element types.
func pgArrayLitToJSON(inner, elem string) string {
	s := strings.TrimSpace(inner)
	s = strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
	if strings.TrimSpace(s) == "" {
		return "[]"
	}
	numeric := isNumericElem(elem)
	var parts []string
	for _, e := range strings.Split(s, ",") {
		e = strings.Trim(strings.TrimSpace(e), `"`)
		switch {
		case strings.EqualFold(e, "null"):
			parts = append(parts, "null")
		case numeric:
			parts = append(parts, e)
		default:
			parts = append(parts, `"`+strings.ReplaceAll(e, `"`, `\"`)+`"`)
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func isNumericElem(elem string) bool {
	d := strings.ToUpper(elem)
	for _, k := range []string{"INT", "REAL", "FLOA", "DOUB", "NUMERIC", "DEC", "SERIAL"} {
		if strings.Contains(d, k) {
			return true
		}
	}
	return false
}

// matchWordAt reports whether w starts at index i in low on a word boundary.
func matchWordAt(low string, i int, w string) bool {
	if i+len(w) > len(low) || low[i:i+len(w)] != w {
		return false
	}
	return i == 0 || !isIdentPart(low[i-1])
}
