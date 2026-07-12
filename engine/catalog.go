package engine

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"strings"
	"sync"

	sqlite "modernc.org/sqlite"
)

// arrayAgg accumulates array_agg() values into a Postgres array literal.
type arrayAgg struct{ vals []string }

func (a *arrayAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) > 0 {
		if args[0] == nil {
			a.vals = append(a.vals, "NULL")
		} else {
			a.vals = append(a.vals, fmt.Sprint(args[0]))
		}
	}
	return nil
}

func (a *arrayAgg) WindowInverse(_ *sqlite.FunctionContext, _ []driver.Value) error {
	if len(a.vals) > 0 {
		a.vals = a.vals[1:]
	}
	return nil
}

func (a *arrayAgg) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) {
	if len(a.vals) == 0 {
		return nil, nil // array_agg over no rows is NULL in Postgres
	}
	return "{" + strings.Join(a.vals, ",") + "}", nil
}

func (a *arrayAgg) Final(_ *sqlite.FunctionContext) {}

// newUUIDv4 returns a random RFC-4122 version-4 UUID string, backing
// gen_random_uuid()/uuid_generate_v4().
func newUUIDv4() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// registerCatalog wires up the minimal Postgres-compatible catalog on the
// SQLite driver. It is process-global (the driver is a singleton) so it runs
// exactly once.
//
// Two pieces:
//   - scalar functions clients commonly call: version(), current_schema(),
//     format_type(), pg_table_is_visible(), plus a REGEXP implementation so
//     Postgres' ~ / !~ operators work.
//   - a connection hook that materializes information_schema and pg_catalog
//     views on every new connection, backed by SQLite's own sqlite_master /
//     pragma_table_info.
//
// information_schema views keep their dotted name (quoted); pg_catalog objects
// use bare names and the Postgres protocol strips the "pg_catalog." qualifier.
// Either way the user's real database file stays free of emulation objects.
var registerCatalog = sync.OnceFunc(func() {
	scalar := func(name string, nArg int32, fn func([]driver.Value) (driver.Value, error)) {
		sqlite.MustRegisterDeterministicScalarFunction(name, nArg,
			func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				return fn(args)
			})
	}
	// scalarND registers a non-deterministic function (e.g. random UUIDs): SQLite
	// must call it once per row, not fold it to a constant.
	scalarND := func(name string, nArg int32, fn func([]driver.Value) (driver.Value, error)) {
		if err := sqlite.RegisterScalarFunction(name, nArg,
			func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
				return fn(args)
			}); err != nil {
			panic(err)
		}
	}

	for _, name := range []string{"gen_random_uuid", "uuid_generate_v4"} {
		scalarND(name, 0, func([]driver.Value) (driver.Value, error) { return newUUIDv4() })
	}

	// Exact numeric: a collation that compares decimal text numerically, plus
	// math/big-backed arithmetic and an exact aggregate sum (see decimal.go).
	sqlite.MustRegisterCollationUtf8("DECIMAL", func(l, r string) int { return decCmp(l, r) })
	scalar("dec_add", 2, func(a []driver.Value) (driver.Value, error) {
		return binDec(a[0], a[1], func(z, x, y *big.Rat) *big.Rat { return z.Add(x, y) })
	})
	scalar("dec_sub", 2, func(a []driver.Value) (driver.Value, error) {
		return binDec(a[0], a[1], func(z, x, y *big.Rat) *big.Rat { return z.Sub(x, y) })
	})
	scalar("dec_mul", 2, func(a []driver.Value) (driver.Value, error) {
		return binDec(a[0], a[1], func(z, x, y *big.Rat) *big.Rat { return z.Mul(x, y) })
	})
	scalar("dec_div", 2, func(a []driver.Value) (driver.Value, error) {
		if y, ok := parseDec(a[1]); ok && y.Sign() == 0 {
			return nil, nil // division by zero -> NULL
		}
		return binDec(a[0], a[1], func(z, x, y *big.Rat) *big.Rat { return z.Quo(x, y) })
	})
	scalar("dec_cmp", 2, func(a []driver.Value) (driver.Value, error) {
		return int64(decCmp(a[0], a[1])), nil
	})
	scalar("dec_round", -1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 {
			return nil, nil
		}
		r, ok := parseDec(a[0])
		if !ok {
			return nil, nil
		}
		scale := 0
		if len(a) > 1 {
			scale = int(asInt64(a[1]))
		}
		return decString(decRound(r, scale)), nil
	})
	sqlite.MustRegisterFunction("dec_sum", &sqlite.FunctionImpl{
		NArgs:         1,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &decSum{}, nil },
	})
	// Override the built-in sum/avg so they are exact over numeric (decimal-text)
	// columns; int/real inputs keep their native return type.
	sqlite.MustRegisterFunction("sum", &sqlite.FunctionImpl{
		NArgs:         1,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &decAgg{}, nil },
	})
	sqlite.MustRegisterFunction("avg", &sqlite.FunctionImpl{
		NArgs:         1,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) { return &decAgg{avg: true}, nil },
	})

	scalar("version", 0, func([]driver.Value) (driver.Value, error) {
		return "PostgreSQL 15.0 (overlite) on overlite, compiled by overlite", nil
	})
	scalar("current_schema", 0, func([]driver.Value) (driver.Value, error) { return "public", nil })
	scalar("current_schemas", -1, func([]driver.Value) (driver.Value, error) { return "{pg_catalog,public}", nil })
	scalar("current_database", 0, func([]driver.Value) (driver.Value, error) { return catalogDBName, nil })
	scalar("current_user", 0, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("session_user", 0, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("pg_get_userbyid", 1, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("pg_table_is_visible", 1, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	// pg_get_expr(expr, relation[, pretty]) renders a stored expression. Our
	// pg_attrdef keeps the default's SQL text as adbin, so returning the first
	// argument yields the column default for pg_dump; other callers pass NULL.
	scalar("pg_get_expr", -1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, nil // NULL in -> NULL out (e.g. a policy with no WITH CHECK)
		}
		return fmt.Sprint(args[0]), nil
	})

	// Definition-rendering helpers psql calls in \d; we don't reconstruct DDL
	// yet, so they return empty strings (variadic arg counts via nArg -1).
	for _, name := range []string{
		"pg_get_indexdef", "pg_get_constraintdef", "pg_get_viewdef",
		"pg_get_partkeydef", "pg_get_ruledef",
		"pg_get_function_identity_arguments", "pg_get_functiondef",
	} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return "", nil })
	}
	// pg_get_triggerdef renders a trigger's CREATE statement (from the registry
	// refreshed per connection); the first argument is the trigger oid.
	scalar("pg_get_triggerdef", -1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return "", nil
		}
		return lookupTriggerDef(asInt64(args[0])), nil
	})
	scalar("pg_relation_is_publishable", -1, func([]driver.Value) (driver.Value, error) { return int64(0), nil })
	scalar("pg_function_is_visible", 1, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	scalar("pg_type_is_visible", 1, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	// obj_description/col_description look up comments; we store none, so NULL.
	for _, name := range []string{"obj_description", "col_description", "shobj_description"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return nil, nil })
	}
	scalar("pg_encoding_to_char", 1, func([]driver.Value) (driver.Value, error) { return "UTF8", nil })
	for _, name := range []string{"pg_get_function_result", "pg_get_function_arguments", "array_to_string"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return "", nil })
	}
	for _, name := range []string{
		"array_ndims", "array_remove", "array_cat", "array_append",
		"array_prepend", "array_position", "array_positions", "array_replace",
	} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return nil, nil })
	}
	// Length over the JSON-array storage. array_length/array_upper of an empty
	// array are NULL (Postgres semantics); cardinality is 0.
	arrLen := func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 {
			return nil, nil
		}
		if n, ok := jsonArrayLen(args[0]); ok && n > 0 {
			return n, nil
		}
		return nil, nil
	}
	scalar("array_length", -1, arrLen)
	scalar("array_upper", -1, arrLen)
	scalar("array_lower", -1, func(args []driver.Value) (driver.Value, error) {
		if len(args) > 0 {
			if n, ok := jsonArrayLen(args[0]); ok && n > 0 {
				return int64(1), nil
			}
		}
		return nil, nil
	})
	// AT TIME ZONE: convert a stored (UTC) instant to a wall clock in the zone.
	scalar("overlite_at_time_zone", 2, func(args []driver.Value) (driver.Value, error) {
		if len(args) != 2 || args[0] == nil || args[1] == nil {
			return nil, nil
		}
		if w, ok := atTimeZone(fmt.Sprint(args[0]), fmt.Sprint(args[1])); ok {
			return w, nil
		}
		return nil, nil
	})
	scalar("cardinality", 1, func(args []driver.Value) (driver.Value, error) {
		if len(args) > 0 {
			if n, ok := jsonArrayLen(args[0]); ok {
				return n, nil
			}
		}
		return int64(0), nil
	})
	// array_agg is a real aggregate (pg_dump collects index/constraint columns
	// with it); it builds a Postgres array literal {a,b,c}.
	sqlite.MustRegisterFunction("array_agg", &sqlite.FunctionImpl{
		NArgs: -1,
		MakeAggregate: func(sqlite.FunctionContext) (sqlite.AggregateFunction, error) {
			return &arrayAgg{}, nil
		},
	})

	// Access-privilege checks: DBeaver filters the object tree with these; we
	// grant everything (single trusted user).
	for _, name := range []string{
		"has_schema_privilege", "has_database_privilege", "has_table_privilege",
		"has_column_privilege", "has_any_column_privilege", "has_sequence_privilege",
		"has_function_privilege", "has_type_privilege", "has_language_privilege",
		"pg_has_role",
	} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	}
	// Partition helpers: we have no partitioned tables, so these return
	// NULL/empty (partition sections of \d then render as absent).
	for _, name := range []string{"pg_partition_ancestors", "pg_partition_root", "pg_partition_tree"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return nil, nil })
	}
	scalar("pg_get_partition_constraintdef", -1, func([]driver.Value) (driver.Value, error) { return "", nil })
	for _, name := range []string{"pg_get_statisticsobjdef", "pg_get_statisticsobjdef_columns"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return "", nil })
	}
	// ACL helpers pg_dump calls; we don't model privileges, so return NULL/empty.
	for _, name := range []string{"acldefault", "aclexplode", "aclitemin", "aclitemout"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return nil, nil })
	}
	scalar("pg_is_in_recovery", 0, func([]driver.Value) (driver.Value, error) { return int64(0), nil })
	scalar("pg_backend_pid", 0, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	scalar("current_setting", -1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 {
			return "", nil
		}
		return currentSetting(fmt.Sprint(args[0])), nil
	})
	scalar("set_config", 3, func(args []driver.Value) (driver.Value, error) {
		if len(args) < 2 {
			return "", nil
		}
		return fmt.Sprint(args[1]), nil // echo the value; we don't apply GUCs
	})

	scalar("quote_ident", 1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, nil
		}
		return quoteIdent(fmt.Sprint(args[0])), nil
	})
	scalar("quote_literal", 1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 || args[0] == nil {
			return nil, nil
		}
		return "'" + strings.ReplaceAll(fmt.Sprint(args[0]), "'", "''") + "'", nil
	})

	scalar("format_type", 2, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 {
			return nil, nil
		}
		oid := asInt64(args[0])
		if name, ok := lookupEnumName(oid); ok {
			return name, nil
		}
		return formatTypeName(oid), nil
	})
	scalar("overlite_type_oid", 1, func(args []driver.Value) (driver.Value, error) {
		if len(args) == 0 {
			return typeOIDText, nil
		}
		return sqliteTypeOID(fmt.Sprint(args[0])), nil
	})

	// jsonb containment (@> / <@, rewritten to json_contains in the dialect); the
	// same operator on a range checks range containment.
	scalar("json_contains", 2, func(args []driver.Value) (driver.Value, error) {
		if len(args) < 2 || args[0] == nil || args[1] == nil {
			return int64(0), nil
		}
		a, b := fmt.Sprint(args[0]), fmt.Sprint(args[1])
		ok := jsonbContains(a, b)
		if isRangeText(a) {
			ok = rangeContains(a, b)
		}
		if ok {
			return int64(1), nil
		}
		return int64(0), nil
	})

	// Range constructors (2 or 3 args): int4range/int8range/numrange/daterange/
	// tsrange/tstzrange all build the same text form.
	for _, name := range []string{"int4range", "int8range", "numrange", "daterange", "tsrange", "tstzrange"} {
		scalar(name, -1, func(a []driver.Value) (driver.Value, error) {
			if len(a) < 2 {
				return nil, nil
			}
			bounds := "[)"
			if len(a) >= 3 && a[2] != nil {
				bounds = fmt.Sprint(a[2])
			}
			return makeRange(argStr(a[0]), argStr(a[1]), bounds), nil
		})
	}
	rangeAcc := func(name string, f func(rangeVal) driver.Value) {
		scalar(name, 1, func(a []driver.Value) (driver.Value, error) {
			if len(a) == 0 || a[0] == nil {
				return nil, nil
			}
			r, ok := parseRange(fmt.Sprint(a[0]))
			if !ok {
				return nil, nil
			}
			return f(r), nil
		})
	}
	boolI := func(b bool) driver.Value {
		if b {
			return int64(1)
		}
		return int64(0)
	}
	rangeAcc("isempty", func(r rangeVal) driver.Value { return boolI(r.empty) })
	rangeAcc("lower_inc", func(r rangeVal) driver.Value { return boolI(!r.empty && r.loInc) })
	rangeAcc("upper_inc", func(r rangeVal) driver.Value { return boolI(!r.empty && r.hiInc) })
	// lower/upper are overloaded: a range yields its bound, otherwise the string
	// case functions (which SQLite provides).
	scalar("lower", 1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 || a[0] == nil {
			return nil, nil
		}
		s := fmt.Sprint(a[0])
		if isRangeText(s) {
			r, _ := parseRange(s)
			if r.empty || r.loInf {
				return nil, nil
			}
			return r.lo, nil
		}
		return strings.ToLower(s), nil
	})
	scalar("upper", 1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 || a[0] == nil {
			return nil, nil
		}
		s := fmt.Sprint(a[0])
		if isRangeText(s) {
			r, _ := parseRange(s)
			if r.empty || r.hiInf {
				return nil, nil
			}
			return r.hi, nil
		}
		return strings.ToUpper(s), nil
	})
	// jsonb key existence (? / ?| / ?&, rewritten from those operators).
	boolFn := func(b bool) driver.Value {
		if b {
			return int64(1)
		}
		return int64(0)
	}
	scalar("jsonb_exists", 2, func(a []driver.Value) (driver.Value, error) {
		if len(a) < 2 || a[0] == nil || a[1] == nil {
			return int64(0), nil
		}
		return boolFn(jsonbExists(fmt.Sprint(a[0]), fmt.Sprint(a[1]))), nil
	})
	scalar("jsonb_exists_any", 2, func(a []driver.Value) (driver.Value, error) {
		if len(a) < 2 || a[0] == nil || a[1] == nil {
			return int64(0), nil
		}
		return boolFn(jsonbExistsAny(fmt.Sprint(a[0]), fmt.Sprint(a[1]))), nil
	})
	scalar("jsonb_exists_all", 2, func(a []driver.Value) (driver.Value, error) {
		if len(a) < 2 || a[0] == nil || a[1] == nil {
			return int64(0), nil
		}
		return boolFn(jsonbExistsAll(fmt.Sprint(a[0]), fmt.Sprint(a[1]))), nil
	})

	// hstore (stored as a JSON object; see engine/hstore.go).
	scalar("hstore_in", 1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 || a[0] == nil {
			return nil, nil
		}
		return hstoreIn(fmt.Sprint(a[0])), nil
	})
	scalar("hstore", 2, func(a []driver.Value) (driver.Value, error) {
		if len(a) < 2 || a[0] == nil {
			return nil, nil
		}
		var v any
		if a[1] != nil {
			v = fmt.Sprint(a[1])
		}
		b, _ := json.Marshal(map[string]any{fmt.Sprint(a[0]): v})
		return string(b), nil
	})
	scalar("akeys", 1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 || a[0] == nil {
			return nil, nil
		}
		return hstoreKeys(fmt.Sprint(a[0])), nil
	})
	scalar("avals", 1, func(a []driver.Value) (driver.Value, error) {
		if len(a) == 0 || a[0] == nil {
			return nil, nil
		}
		return hstoreVals(fmt.Sprint(a[0])), nil
	})
	// hstore is already JSON, so hstore_to_json[b] is the identity.
	for _, name := range []string{"hstore_to_json", "hstore_to_jsonb"} {
		scalar(name, 1, func(a []driver.Value) (driver.Value, error) {
			if len(a) == 0 {
				return nil, nil
			}
			return a[0], nil
		})
	}

	// Date/time functions (now()/extract are handled in the dialect layer).
	scalar("date_trunc", 2, dateTruncFn)
	scalar("date_part", 2, datePartFn)
	scalar("to_char", 2, toCharFn)
	scalar("age", -1, ageFn)

	scalar("regexp", 2, func(args []driver.Value) (driver.Value, error) {
		if len(args) != 2 {
			return nil, fmt.Errorf("regexp: want 2 args")
		}
		re, err := regexp.Compile(fmt.Sprint(args[0]))
		if err != nil {
			return nil, err
		}
		if re.MatchString(fmt.Sprint(args[1])) {
			return int64(1), nil
		}
		return int64(0), nil
	})

	sqlite.RegisterConnectionHook(func(conn sqlite.ExecQuerierContext, dsn string) error {
		ctx := context.Background()
		exec := func(q string) error {
			_, err := conn.ExecContext(ctx, q, nil)
			return err
		}
		query := func(q string) ([]string, error) {
			rows, err := conn.QueryContext(ctx, q, nil)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			vals := make([]driver.Value, len(rows.Columns()))
			var out []string
			for {
				if err := rows.Next(vals); err != nil {
					if err == io.EOF {
						break
					}
					return nil, err
				}
				out = append(out, fmt.Sprint(vals[0]))
			}
			return out, nil
		}
		return setupConnection(ctx, exec, query, mainDBPath(dsn))
	})
})

// Postgres type OIDs used by the emulated pg_type / atttypid mapping.
const (
	typeOIDBool      = 16
	typeOIDBytea     = 17
	typeOIDInt8      = 20
	typeOIDInt4      = 23
	typeOIDText      = 25
	typeOIDFloat8    = 701
	typeOIDNumeric   = 1700
	typeOIDDate      = 1082
	typeOIDTimestamp = 1114
	typeOIDJSON      = 114
	typeOIDJSONB     = 3802
	typeOIDUUID      = 2950
)

// sqliteTypeOID maps a SQLite declared type to a Postgres type OID using
// SQLite's affinity rules (substring matching).
func sqliteTypeOID(decl string) int64 {
	d := strings.ToUpper(decl)
	switch {
	case strings.Contains(d, "DECIMALTEXT"): // overlite's exact-numeric storage type
		return typeOIDNumeric
	case strings.Contains(d, "UUID"):
		return typeOIDUUID
	case strings.Contains(d, "JSONB"):
		return typeOIDJSONB
	case strings.Contains(d, "JSON"):
		return typeOIDJSON
	case strings.Contains(d, "INT"):
		return typeOIDInt4
	case strings.Contains(d, "CHAR"), strings.Contains(d, "CLOB"), strings.Contains(d, "TEXT"):
		return typeOIDText
	case strings.Contains(d, "BLOB"):
		return typeOIDBytea
	case strings.Contains(d, "REAL"), strings.Contains(d, "FLOA"), strings.Contains(d, "DOUB"):
		return typeOIDFloat8
	case strings.Contains(d, "BOOL"):
		return typeOIDBool
	case strings.Contains(d, "NUMERIC"), strings.Contains(d, "DEC"):
		return typeOIDNumeric
	case strings.Contains(d, "TIMESTAMP"), strings.Contains(d, "DATETIME"):
		return typeOIDTimestamp
	case strings.Contains(d, "DATE"):
		return typeOIDDate
	case strings.Contains(d, "TIME"):
		return typeOIDTimestamp
	default:
		return typeOIDText
	}
}

// formatTypeName renders a type OID as Postgres' canonical display name, as
// format_type() would.
func formatTypeName(oid int64) string {
	switch oid {
	case typeOIDBool:
		return "boolean"
	case typeOIDBytea:
		return "bytea"
	case typeOIDInt8:
		return "bigint"
	case typeOIDInt4:
		return "integer"
	case typeOIDText:
		return "text"
	case typeOIDFloat8:
		return "double precision"
	case typeOIDNumeric:
		return "numeric"
	case typeOIDDate:
		return "date"
	case typeOIDTimestamp:
		return "timestamp without time zone"
	case typeOIDJSON:
		return "json"
	case typeOIDJSONB:
		return "jsonb"
	case typeOIDUUID:
		return "uuid"
	default:
		return "text"
	}
}

// currentSetting backs the current_setting() function with the same values the
// SHOW command reports.
func currentSetting(name string) string {
	switch name {
	case "server_version":
		return "15.0"
	case "server_version_num":
		return "150000"
	case "server_encoding", "client_encoding":
		return "UTF8"
	case "standard_conforming_strings", "integer_datetimes", "is_superuser":
		return "on"
	case "search_path":
		return `"$user", public`
	case "transaction_isolation", "default_transaction_isolation":
		return "read committed"
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

// reBareIdent is a lowercase identifier that needs no double-quoting.
var reBareIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// quoteIdent quotes an SQL identifier the way Postgres' quote_ident() does:
// bare if it is already a safe lowercase identifier, otherwise double-quoted
// (with embedded quotes doubled).
func quoteIdent(s string) string {
	if reBareIdent.MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func asInt64(v driver.Value) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case float64:
		return int64(n)
	default:
		return 0
	}
}

// staticCatalogViews are schema-independent; created on every connection.
// The schema-spanning views (pg_namespace/pg_class/pg_attribute/pg_index/
// pg_constraint/information_schema.*) are generated in catalog_views.go.
var staticCatalogViews = []string{
	// The outer SELECT adds the columns \dT+ and pg_dump read (typacl,
	// typispreferred, typisdefined, ...) without touching every UNION row.
	`CREATE TEMP VIEW IF NOT EXISTS pg_type AS
	 SELECT ov_t.*, 1247 AS tableoid, NULL AS typacl, 0 AS typispreferred, 1 AS typisdefined,
	        '-' AS typalign, 'p' AS typstorage, 0 AS typrelid2 FROM (
	 SELECT 16 AS oid, 'bool' AS typname, 11 AS typnamespace, 10 AS typowner,
	        'b' AS typtype, 'B' AS typcategory, 1 AS typlen, 0 AS typbyval,
	        0 AS typrelid, 0 AS typelem, 1000 AS typarray, 0 AS typnotnull,
	        0 AS typbasetype, -1 AS typtypmod, 0 AS typndims, 0 AS typcollation,
	        NULL AS typdefault, ',' AS typdelim
	 UNION ALL SELECT 17,   'bytea',     11, 10, 'b', 'U', -1, 0, 0, 0, 1001, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 20,   'int8',      11, 10, 'b', 'N', 8,  1, 0, 0, 1016, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 21,   'int2',      11, 10, 'b', 'N', 2,  1, 0, 0, 1005, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 23,   'int4',      11, 10, 'b', 'N', 4,  1, 0, 0, 1007, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 25,   'text',      11, 10, 'b', 'S', -1, 0, 0, 0, 1009, 0, 0, -1, 0, 100, NULL, ','
	 UNION ALL SELECT 701,  'float8',    11, 10, 'b', 'N', 8,  1, 0, 0, 1022, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 1043, 'varchar',   11, 10, 'b', 'S', -1, 0, 0, 0, 1015, 0, 0, -1, 0, 100, NULL, ','
	 UNION ALL SELECT 1082, 'date',      11, 10, 'b', 'D', 4,  1, 0, 0, 1182, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 1114, 'timestamp', 11, 10, 'b', 'D', 8,  1, 0, 0, 1115, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 1700, 'numeric',   11, 10, 'b', 'N', -1, 0, 0, 0, 1231, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 114,  'json',      11, 10, 'b', 'U', -1, 0, 0, 0, 199,  0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 3802, 'jsonb',     11, 10, 'b', 'U', -1, 0, 0, 0, 3807, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT 2950, 'uuid',      11, 10, 'b', 'U', 16, 0, 0, 0, 2951, 0, 0, -1, 0, 0, NULL, ','
	 UNION ALL SELECT CAST(rowid + 90000000 AS INTEGER), typname, 2200, 10, 'e', 'E', 4, 1, 0, 0, 0, 0, 0, -1, 0, 0, NULL, ','
	           FROM _overlite_enum_types
	 UNION ALL SELECT CAST(rowid + 95000000 AS INTEGER), typname, 2200, 10, 'c', 'C', -1, 0, 0, 0, 0, 0, 0, -1, 0, 0, NULL, ','
	           FROM _overlite_composite_types
	 ) ov_t`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_enum AS
	 SELECT CAST(e.rowid AS INTEGER) AS oid,
	        CAST((SELECT t.rowid + 90000000 FROM _overlite_enum_types t WHERE t.typname = e.typname) AS INTEGER) AS enumtypid,
	        CAST(e.sortorder AS REAL) AS enumsortorder, e.label AS enumlabel
	 FROM _overlite_enums e`,

	// pg_auth_members: real role membership from _overlite_memberships, with oids
	// resolved through pg_roles (so psql's \du "Member of" works).
	`CREATE TEMP VIEW IF NOT EXISTS pg_auth_members AS
	 SELECT m.rowid AS oid, ro.oid AS roleid, mo.oid AS member,
	        10 AS grantor, m.admin_option AS admin_option
	 FROM _overlite_memberships m
	 JOIN pg_roles ro ON lower(ro.rolname) = lower(m.roleof)
	 JOIN pg_roles mo ON lower(mo.rolname) = lower(m.member)`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_tablespace AS
	 SELECT 1663 AS oid, 'pg_default' AS spcname, 10 AS spcowner, NULL AS spcacl, NULL AS spcoptions
	 UNION ALL SELECT 1664, 'pg_global', 10, NULL, NULL`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_collation AS
	 SELECT 0 AS oid, '' AS collname, 11 AS collnamespace, 0 AS collowner,
	        'c' AS collprovider, 1 AS collisdeterministic, -1 AS collencoding,
	        '' AS collcollate, '' AS collctype, NULL AS collversion
	 WHERE 0`,

	// Access methods: \dt LEFT JOINs pg_am on c.relam.
	`CREATE TEMP VIEW IF NOT EXISTS pg_am AS
	 SELECT 2   AS oid, 'heap'  AS amname, 'heap_tableam_handler' AS amhandler, 't' AS amtype
	 UNION ALL SELECT 403, 'btree', 'bthandler',   'i'
	 UNION ALL SELECT 405, 'hash',  'hashhandler', 'i'`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_description AS
	 SELECT 0 AS objoid, 0 AS classoid, 0 AS objsubid, '' AS description
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_inherits AS
	 SELECT 0 AS inhrelid, 0 AS inhparent, 0 AS inhseqno, 0 AS inhdetachpending
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_rewrite AS
	 SELECT 0 AS oid, '' AS rulename, 0 AS ev_class, '1' AS ev_type,
	        'O' AS ev_enabled, 0 AS is_instead, NULL AS ev_qual, NULL AS ev_action
	 WHERE 0`,

	// pg_policy: real RLS policies from _overlite_policies. polcmd maps the
	// command to Postgres' single-char code; polqual/polwithcheck carry the
	// expression text (what psql renders for \d). polroles NULL means "to all".
	`CREATE TEMP VIEW IF NOT EXISTS pg_policy AS
	 SELECT p.rowid AS oid, p.polname AS polname,
	        (SELECT c.oid FROM pg_class c WHERE lower(c.relname) = lower(p.tablename)
	          AND c.relkind IN ('r','p') LIMIT 1) AS polrelid,
	        CASE upper(p.command) WHEN 'SELECT' THEN 'r' WHEN 'INSERT' THEN 'a'
	          WHEN 'UPDATE' THEN 'w' WHEN 'DELETE' THEN 'd' ELSE '*' END AS polcmd,
	        p.permissive AS polpermissive, NULL AS polroles,
	        CASE WHEN p.using_expr = '' THEN NULL ELSE p.using_expr END AS polqual,
	        CASE WHEN p.check_expr = '' THEN NULL ELSE p.check_expr END AS polwithcheck
	 FROM _overlite_policies p`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_statistic_ext AS
	 SELECT 0 AS oid, 0 AS stxrelid, '' AS stxname, 2200 AS stxnamespace,
	        10 AS stxowner, '' AS stxkeys, '' AS stxkind, -1 AS stxstattarget
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_partitioned_table AS
	 SELECT 0 AS partrelid, 'r' AS partstrat, 0 AS partnatts, 0 AS partdefid,
	        '' AS partattrs, '' AS partclass, '' AS partcollation, NULL AS partexprs
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_publication AS
	 SELECT 0 AS oid, '' AS pubname, 10 AS pubowner, 0 AS puballtables,
	        0 AS pubinsert, 0 AS pubupdate, 0 AS pubdelete, 0 AS pubtruncate,
	        0 AS pubviaroot
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_publication_rel AS
	 SELECT 0 AS oid, 0 AS prpubid, 0 AS prrelid, NULL AS prqual, NULL AS prattrs
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_publication_namespace AS
	 SELECT 0 AS oid, 0 AS pnpubid, 0 AS pnnspid
	 WHERE 0`,

	// Empty system catalogs pg_dump probes early (extensions, dependencies,
	// default privileges, comments). Kept empty so its queries run and return
	// nothing.
	`CREATE TEMP VIEW IF NOT EXISTS pg_extension AS
	 SELECT 0 AS oid, '' AS extname, 10 AS extowner, 2200 AS extnamespace,
	        0 AS extrelocatable, '' AS extversion, NULL AS extconfig, NULL AS extcondition
	 WHERE 0`,
	// pg_depend: real structural dependencies, assembled from the other catalog
	// views so the oids line up by construction. Each index / constraint /
	// trigger / policy auto-depends ('a') on the table it belongs to.
	`CREATE TEMP VIEW IF NOT EXISTS pg_depend AS
	 SELECT 1259 AS classid, i.indexrelid AS objid, 0 AS objsubid,
	        1259 AS refclassid, i.indrelid AS refobjid, 0 AS refobjsubid, 'a' AS deptype
	 FROM pg_index i
	 UNION ALL SELECT 2606, c.oid, 0, 1259, c.conrelid, 0, 'a' FROM pg_constraint c
	 UNION ALL SELECT 2620, t.oid, 0, 1259, t.tgrelid, 0, 'a' FROM pg_trigger t
	 UNION ALL SELECT 3256, p.oid, 0, 1259, p.polrelid, 0, 'a' FROM pg_policy p`,
	// pg_shdepend: shared dependency of each table on its owning role.
	`CREATE TEMP VIEW IF NOT EXISTS pg_shdepend AS
	 SELECT 0 AS dbid, 1259 AS classid, c.oid AS objid, 0 AS objsubid,
	        1260 AS refclassid, r.oid AS refobjid, 'o' AS deptype
	 FROM _overlite_owners o
	 JOIN pg_class c ON lower(c.relname) = lower(o.tablename) AND c.relkind IN ('r','p')
	 JOIN pg_roles r ON lower(r.rolname) = lower(o.owner)`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_default_acl AS
	 SELECT 0 AS oid, 0 AS defaclrole, 2200 AS defaclnamespace, 'r' AS defaclobjtype, NULL AS defaclacl
	 WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_init_privs AS
	 SELECT 0 AS objoid, 0 AS classoid, 0 AS objsubid, 'i' AS privtype, NULL AS initprivs
	 WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_shdescription AS
	 SELECT 0 AS objoid, 0 AS classoid, '' AS description WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_largeobject_metadata AS
	 SELECT 0 AS oid, 0 AS lomowner, NULL AS lomacl WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_subscription AS
	 SELECT 0 AS oid, 0 AS subdbid, '' AS subname WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_event_trigger AS
	 SELECT 0 AS oid, '' AS evtname, '' AS evtevent, 10 AS evtowner, 'O' AS evtenabled,
	        0 AS evtfoid, NULL AS evttags WHERE 0`,

	// Foreign-data catalogs: we have none, but pg_dump joins them per relation.
	`CREATE TEMP VIEW IF NOT EXISTS pg_foreign_table AS
	 SELECT 0 AS ftrelid, 0 AS ftserver, NULL AS ftoptions WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_foreign_server AS
	 SELECT 0 AS oid, '' AS srvname, 10 AS srvowner, 0 AS srvfdw, NULL AS srvtype,
	        NULL AS srvversion, NULL AS srvacl, NULL AS srvoptions WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_foreign_data_wrapper AS
	 SELECT 0 AS oid, '' AS fdwname, 10 AS fdwowner, 0 AS fdwhandler, 0 AS fdwvalidator,
	        NULL AS fdwacl, NULL AS fdwoptions WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_transform AS
	 SELECT 0 AS oid, 0 AS trftype, 0 AS trflang, 0 AS trffromsql, 0 AS trftosql WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_operator AS
	 SELECT 0 AS oid, '' AS oprname, 11 AS oprnamespace, 10 AS oprowner, 'b' AS oprkind,
	        0 AS oprleft, 0 AS oprright, 0 AS oprresult, 0 AS oprcom, 0 AS oprnegate,
	        0 AS oprcode WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_opclass AS
	 SELECT 0 AS oid, 0 AS opcmethod, '' AS opcname, 11 AS opcnamespace, 10 AS opcowner,
	        0 AS opcfamily, 0 AS opcintype, 1 AS opcdefault, 0 AS opckeytype WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_opfamily AS
	 SELECT 0 AS oid, 0 AS opfmethod, '' AS opfname, 11 AS opfnamespace, 10 AS opfowner WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_conversion AS
	 SELECT 0 AS oid, '' AS conname, 11 AS connamespace, 10 AS conowner,
	        0 AS conforencoding, 0 AS contoencoding, 0 AS conproc, 0 AS condefault WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_aggregate AS
	 SELECT 0 AS aggfnoid, 'n' AS aggkind, 0 AS aggnumdirectargs, 0 AS aggtransfn,
	        0 AS aggfinalfn, 0 AS aggtranstype WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_range AS
	 SELECT 0 AS rngtypid, 0 AS rngsubtype, 0 AS rngmultitypid, 0 AS rngcollation,
	        0 AS rngsubopc, 0 AS rngcanonical, 0 AS rngsubdiff WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_ts_config AS
	 SELECT 0 AS oid, '' AS cfgname, 11 AS cfgnamespace, 10 AS cfgowner, 0 AS cfgparser WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_ts_dict AS
	 SELECT 0 AS oid, '' AS dictname, 11 AS dictnamespace, 10 AS dictowner,
	        0 AS dicttemplate, NULL AS dictinitoption WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_ts_parser AS
	 SELECT 0 AS oid, '' AS prsname, 11 AS prsnamespace, 0 AS prsstart, 0 AS prstoken,
	        0 AS prsend, 0 AS prsheadline, 0 AS prslextype WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_ts_template AS
	 SELECT 0 AS oid, '' AS tmplname, 11 AS tmplnamespace, 0 AS tmplinit, 0 AS tmpllexize WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_seclabels AS
	 SELECT 0 AS objoid, 0 AS classoid, 0 AS objsubid, '' AS objtype, 0 AS objnamespace,
	        '' AS objname, '' AS provider, '' AS label WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_seclabel AS
	 SELECT 0 AS objoid, 0 AS classoid, 0 AS objsubid, '' AS provider, '' AS label WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_shseclabel AS
	 SELECT 0 AS objoid, 0 AS classoid, '' AS provider, '' AS label WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_amproc AS
	 SELECT 0 AS oid, 0 AS amprocfamily, 0 AS amproclefttype, 0 AS amprocrighttype,
	        0 AS amprocnum, 0 AS amproc WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_amop AS
	 SELECT 0 AS oid, 0 AS amopfamily, 0 AS amoplefttype, 0 AS amoprighttype, 0 AS amopstrategy,
	        'a' AS amoppurpose, 0 AS amopopr, 0 AS amopmethod, 0 AS amopsortfamily WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_cast AS
	 SELECT 0 AS oid, 0 AS castsource, 0 AS casttarget, 0 AS castfunc,
	        'e' AS castcontext, 'f' AS castmethod WHERE 0`,
	`CREATE TEMP VIEW IF NOT EXISTS pg_language AS
	 SELECT 0 AS oid, '' AS lanname, 10 AS lanowner, 0 AS lanispl, 0 AS lanpltrusted,
	        0 AS lanplcallfoid, 0 AS laninline, 0 AS lanvalidator, NULL AS lanacl WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_settings AS
	 SELECT '' AS name, '' AS setting, '' AS unit, '' AS category, '' AS short_desc,
	        '' AS context, '' AS vartype, '' AS source, NULL AS min_val, NULL AS max_val,
	        NULL AS enumvals, '' AS boot_val, '' AS reset_val
	 WHERE 0`,

	// Sequence metadata for \d <seq> and pg_dump (rows come from
	// _overlite_sequences; oids match the pg_class 'S' rows).
	`CREATE TEMP VIEW IF NOT EXISTS pg_sequence AS
	 SELECT CAST(rowid + 80000000 AS INTEGER) AS seqrelid, 20 AS seqtypid,
	        start_value AS seqstart, increment AS seqincrement, max_value AS seqmax,
	        min_value AS seqmin, cache_size AS seqcache, is_cycled AS seqcycle
	 FROM _overlite_sequences`,
}

// catalogFunctionNames are the functions overlite provides; they populate
// pg_proc and information_schema.routines so \df and tools can list them.
var catalogFunctionNames = []string{
	"version", "now", "age",
	"current_schema", "current_schemas", "current_database", "current_user",
	"session_user", "current_setting", "format_type", "quote_ident", "quote_literal",
	"date_trunc", "date_part", "to_char",
	"gen_random_uuid", "uuid_generate_v4",
	"array_to_string", "array_agg", "array_length",
	"json_contains",
	"pg_get_indexdef", "pg_get_constraintdef", "pg_get_triggerdef", "pg_get_expr",
	"has_table_privilege", "has_schema_privilege",
}

// pgProcView builds pg_proc from the provided-function list (all in pg_catalog).
func pgProcView() string {
	const row = `SELECT %d AS oid, %s AS proname, 11 AS pronamespace, 10 AS proowner,` +
		` 12 AS prolang, 'f' AS prokind, 25 AS prorettype, '' AS proargtypes,` +
		` 0 AS pronargs, 0 AS proretset, NULL AS proacl, '' AS prosrc, NULL AS probin,` +
		` 'v' AS provolatile, 0 AS proisstrict, NULL AS proargmodes, NULL AS proargnames,` +
		` NULL AS proallargtypes, 0 AS prosecdef, NULL AS proconfig, 100 AS procost,` +
		` 0 AS prorows, 0 AS provariadic, 0 AS prosupport, 0 AS proleakproof,` +
		` 's' AS proparallel, NULL AS proargdefaults, 0 AS pronargdefaults, '' AS prosqlbody,` +
		` 1255 AS tableoid`
	parts := make([]string, len(catalogFunctionNames))
	for i, name := range catalogFunctionNames {
		parts[i] = fmt.Sprintf(row, 100000+i, sqlQuote(name))
	}
	return "CREATE TEMP VIEW pg_proc AS " + strings.Join(parts, " UNION ALL ")
}

// infoRoutinesView builds information_schema.routines from the same list.
func infoRoutinesView() string {
	const row = `SELECT %[2]s AS specific_catalog, 'pg_catalog' AS specific_schema,` +
		` %[1]s || '_' || %[3]d AS specific_name, %[2]s AS routine_catalog,` +
		` 'pg_catalog' AS routine_schema, %[1]s AS routine_name, 'FUNCTION' AS routine_type,` +
		` 'text' AS data_type, NULL AS routine_definition`
	cat := sqlQuote(catalogDBName)
	parts := make([]string, len(catalogFunctionNames))
	for i, name := range catalogFunctionNames {
		parts[i] = fmt.Sprintf(row, sqlQuote(name), cat, 100000+i)
	}
	return `CREATE TEMP VIEW "information_schema.routines" AS ` + strings.Join(parts, " UNION ALL ")
}
