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
	oidBool        = 16
	oidBytea       = 17
	oidInt8        = 20
	oidInt2        = 21
	oidInt4        = 23
	oidText        = 25
	oidJSON        = 114
	oidFloat8      = 701
	oidBpchar      = 1042
	oidVarchar     = 1043
	oidDate        = 1082
	oidTime        = 1083
	oidNumeric     = 1700
	oidTimestamp   = 1114
	oidTimestamptz = 1184
	oidUUID        = 2950
	oidJSONB       = 3802
)

// strictDeclOID maps a declared type we can advertise faithfully (and encode in
// both text and binary) straight from the column's declared type, before value
// sampling. Integers/floats/numeric are intentionally left to sampling: this
// SQLite-backed catalog stores oids wider than int4, so all integers advertise
// as int8.
func strictDeclOID(decl string) (uint32, bool) {
	d := strings.ToUpper(decl)
	switch {
	case d == "":
		return 0, false
	case strings.Contains(d, "BOOL"):
		return oidBool, true
	case strings.Contains(d, "JSONB"):
		return oidJSONB, true
	case strings.Contains(d, "JSON"):
		return oidJSON, true
	case strings.Contains(d, "UUID"):
		return oidUUID, true
	case strings.Contains(d, "VARCHAR"), strings.Contains(d, "CHARACTER VARYING"):
		return oidVarchar, true
	case strings.Contains(d, "CHAR"), strings.Contains(d, "BPCHAR"):
		return oidBpchar, true
	}
	return 0, false
}

// timeOID picks date/time/timestamp based on a column's declared type.
func timeOID(decl string) uint32 {
	d := strings.ToUpper(decl)
	switch {
	case strings.Contains(d, "TIMESTAMPTZ"),
		strings.Contains(d, "TIMESTAMP") && strings.Contains(d, "WITH TIME ZONE"):
		return oidTimestamptz
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
	"rolbypassrls":  true,
	"datistemplate": true, "datallowconn": true,
	// Boolean expression aliases pg_dump reads as t/f (SQLite returns 1/0 for
	// these computed columns, which have no declared type).
	"notnull_islocal": true, "notnull_noinherit": true, "notnull_invalidoid": true,
	// A sequence read as a relation: is_called is boolean.
	"is_called": true,
}

// oidForColumn picks an OID for a column. SQLite is dynamically typed, so we
// prefer the runtime Go type of the first non-NULL value in the column and
// fall back to the declared type affinity.
func oidForColumn(col core.Column, rows [][]core.Value, idx int) uint32 {
	if boolCatalogColumns[col.Name] {
		return oidBool
	}
	// Array columns carry their element type in the declared type (`text[]`).
	if isArrayDecl(col.DeclType) {
		return arrayOIDForDecl(col.DeclType)
	}
	// timestamptz is stored as text, so sampling would see a string; trust the
	// declared type instead.
	if isTimestamptzDecl(col.DeclType) {
		return oidTimestamptz
	}
	// A faithfully-mappable declared type wins over value sampling (which would
	// e.g. see a boolean's 0/1 as int, or a json string as text).
	if oid, ok := strictDeclOID(col.DeclType); ok {
		return oid
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

// boolish reports whether v represents a true boolean, accepting the several
// forms SQLite may hold for a boolean column (int 0/1, Go bool, or a stored
// string like 't'/'true'/'yes').
func boolish(v core.Value) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "t", "true", "1", "y", "yes", "on":
			return true
		}
		return false
	default:
		return toInt64(v) != 0
	}
}

// encodeText renders a value in Postgres text format, or returns nil to signal
// SQL NULL (encoded on the wire as a -1 length).
func encodeText(oid uint32, v core.Value) []byte {
	if v == nil {
		return nil
	}
	// Array columns hold a JSON array in a TEXT cell; render Postgres' `{…}`.
	if isArrayOID(oid) {
		return jsonArrayToPGText(v)
	}
	// timestamptz is stored as bare UTC text; advertise the +00 offset so clients
	// read it as an absolute instant.
	if oid == oidTimestamptz {
		if s, ok := v.(string); ok {
			return []byte(withUTCOffset(s))
		}
	}
	// Boolean columns may arrive as 0/1 integers or a stored 't'/'true' string;
	// normalize to the Postgres 't'/'f' text form.
	if oid == oidBool {
		if boolish(v) {
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
		case oidTimestamptz:
			return []byte(val.UTC().Format("2006-01-02 15:04:05.999999-07"))
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
	case oidJSONB:
		// jsonb binary format is a 1-byte version (1) followed by the JSON text.
		return append([]byte{1}, encodeText(oidText, v)...)
	case oidUUID:
		if raw, ok := uuidBytes(v); ok {
			return raw
		}
		return encodeText(oidText, v)
	default: // text, json, varchar, bpchar, ...: UTF-8 bytes
		return encodeText(oidText, v)
	}
}

// uuidBytes parses a canonical UUID string into its 16 raw bytes.
func uuidBytes(v core.Value) ([]byte, bool) {
	s, ok := v.(string)
	if !ok {
		return nil, false
	}
	hexOnly := strings.ReplaceAll(s, "-", "")
	if len(hexOnly) != 32 {
		return nil, false
	}
	raw, err := hex.DecodeString(hexOnly)
	if err != nil {
		return nil, false
	}
	return raw, true
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
