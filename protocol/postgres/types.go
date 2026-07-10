package postgres

import (
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"overlite/core"
)

// Result format codes carried in Bind.
const (
	formatText   = 0
	formatBinary = 1
)

// Postgres type OIDs we advertise. We always send values in text format, so
// these only need to be accurate enough for a client to scan into the right
// Go type.
const (
	oidBool      = 16
	oidBytea     = 17
	oidInt8      = 20
	oidInt2      = 21
	oidInt4      = 23
	oidText      = 25
	oidFloat8    = 701
	oidDate      = 1082
	oidTime      = 1083
	oidNumeric   = 1700
	oidTimestamp = 1114
)

// timeOID picks date/time/timestamp based on a column's declared type.
func timeOID(decl string) uint32 {
	d := strings.ToUpper(decl)
	switch {
	case strings.Contains(d, "TIMESTAMP"), strings.Contains(d, "DATETIME"):
		return oidTimestamp
	case strings.Contains(d, "DATE"):
		return oidDate
	case strings.Contains(d, "TIME"):
		return oidTime
	default:
		return oidTimestamp
	}
}

// boolCatalogColumns are pg_catalog columns that are boolean in PostgreSQL but
// surface as 0/1 integers from SQLite. Clients type them by the OID we send, so
// we must advertise bool for them to be scannable as bool.
var boolCatalogColumns = map[string]bool{
	"attnotnull": true, "atthasdef": true, "atthasmissing": true,
	"attbyval": true, "attisdropped": true, "attislocal": true,
	"relhasindex": true, "relisshared": true, "relhasrules": true,
	"relhastriggers": true, "relhassubclass": true, "relrowsecurity": true,
	"relforcerowsecurity": true, "relispopulated": true, "relispartition": true,
	"typnotnull": true, "typbyval": true,
	"indisprimary": true, "indisunique": true, "indisclustered": true,
	"indisvalid": true, "indisexclusion": true, "indimmediate": true,
	"indisready": true, "indislive": true, "indisreplident": true,
	"conislocal": true, "convalidated": true, "connoinherit": true,
	"condeferrable": true, "condeferred": true,
	"rolsuper": true, "rolinherit": true, "rolcreaterole": true,
	"rolcreatedb": true, "rolcanlogin": true, "rolreplication": true,
	"rolbypassrls": true,
	"datistemplate": true, "datallowconn": true,
}

// oidForColumn picks an OID for a column. SQLite is dynamically typed, so we
// prefer the runtime Go type of the first non-NULL value in the column and
// fall back to the declared type affinity.
func oidForColumn(col core.Column, rows [][]core.Value, idx int) uint32 {
	if boolCatalogColumns[col.Name] {
		return oidBool
	}
	for _, row := range rows {
		if idx >= len(row) || row[idx] == nil {
			continue
		}
		switch v := row[idx].(type) {
		case bool:
			return oidBool
		case int64, int32, int16, int8, int:
			return oidInt8
		case float64, float32:
			return oidFloat8
		case time.Time:
			return timeOID(col.DeclType)
		case []byte:
			if isTextAffinity(col.DeclType) {
				return oidText
			}
			return oidBytea
		case string:
			return oidText
		default:
			_ = v
			return oidText
		}
	}
	return oidFromDeclType(col.DeclType)
}

// oidFromDeclType maps a SQLite declared type to an OID using SQLite's type
// affinity rules (substring matching), for columns with no sampled value.
func oidFromDeclType(decl string) uint32 {
	d := strings.ToUpper(decl)
	switch {
	case d == "":
		return oidText
	case strings.Contains(d, "INT"): // INTEGER, INT, int64
		return oidInt8
	case strings.Contains(d, "CHAR"), strings.Contains(d, "CLOB"),
		strings.Contains(d, "TEXT"), strings.Contains(d, "STRING"):
		return oidText
	case strings.Contains(d, "BLOB"):
		return oidBytea
	case strings.Contains(d, "REAL"), strings.Contains(d, "FLOA"), strings.Contains(d, "DOUB"): // incl. float64
		return oidFloat8
	case strings.Contains(d, "BOOL"):
		return oidBool
	case strings.Contains(d, "NUMERIC"), strings.Contains(d, "DEC"):
		return oidNumeric
	case strings.Contains(d, "DATE"), strings.Contains(d, "TIME"):
		return timeOID(d)
	default:
		return oidText
	}
}

func isTextAffinity(decl string) bool {
	d := strings.ToUpper(decl)
	return strings.Contains(d, "CHAR") || strings.Contains(d, "CLOB") || strings.Contains(d, "TEXT")
}

// encodeText renders a value in Postgres text format, or returns nil to signal
// SQL NULL (encoded on the wire as a -1 length).
func encodeText(oid uint32, v core.Value) []byte {
	if v == nil {
		return nil
	}
	// Boolean columns may arrive as 0/1 integers from SQLite; normalize to the
	// Postgres 't'/'f' text form.
	if oid == oidBool {
		if toInt64(v) != 0 {
			return []byte("t")
		}
		return []byte("f")
	}
	switch val := v.(type) {
	case bool:
		if val {
			return []byte("t")
		}
		return []byte("f")
	case []byte:
		if oid == oidBytea {
			return []byte(`\x` + hex.EncodeToString(val))
		}
		return val
	case string:
		return []byte(val)
	case time.Time:
		switch oid {
		case oidDate:
			return []byte(val.Format("2006-01-02"))
		case oidTime:
			return []byte(val.Format("15:04:05.999999"))
		default:
			return []byte(val.Format("2006-01-02 15:04:05.999999"))
		}
	default:
		return []byte(fmt.Sprint(val))
	}
}

// encodeValue renders a value in the requested wire format for the given OID.
// It returns nil for SQL NULL (a -1 length on the wire).
func encodeValue(format int, oid uint32, v core.Value) []byte {
	if v == nil {
		return nil
	}
	if format == formatText {
		return encodeText(oid, v)
	}
	return encodeBinary(oid, v)
}

// encodeBinary renders a value in Postgres binary format for the given OID. The
// byte layout must match the OID we advertised in the row description, so we
// coerce the value to the OID's expected Go shape.
func encodeBinary(oid uint32, v core.Value) []byte {
	switch oid {
	case oidBool:
		if toInt64(v) != 0 {
			return []byte{1}
		}
		return []byte{0}
	case oidInt2:
		return appendUint16(nil, uint16(toInt64(v)))
	case oidInt4:
		return appendUint32(nil, uint32(toInt64(v)))
	case oidInt8:
		return appendUint64(nil, uint64(toInt64(v)))
	case oidFloat8:
		return appendUint64(nil, math.Float64bits(toFloat64(v)))
	case oidBytea:
		if b, ok := v.([]byte); ok {
			return b
		}
		return []byte(fmt.Sprint(v))
	default: // text and everything else: UTF-8 bytes
		return encodeText(oidText, v)
	}
}

func toInt64(v core.Value) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int32:
		return int64(n)
	case int:
		return int64(n)
	case float64:
		return int64(n)
	case bool:
		if n {
			return 1
		}
		return 0
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	case []byte:
		i, _ := strconv.ParseInt(strings.TrimSpace(string(n)), 10, 64)
		return i
	default:
		return 0
	}
}

func toFloat64(v core.Value) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f
	case []byte:
		f, _ := strconv.ParseFloat(strings.TrimSpace(string(n)), 64)
		return f
	default:
		return 0
	}
}
