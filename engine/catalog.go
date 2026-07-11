package engine

import (
	"context"
	"crypto/rand"
	"database/sql/driver"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	sqlite "modernc.org/sqlite"
)

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

	scalar("version", 0, func([]driver.Value) (driver.Value, error) {
		return "PostgreSQL 15.0 (overlite) on overlite, compiled by overlite", nil
	})
	scalar("current_schema", 0, func([]driver.Value) (driver.Value, error) { return "public", nil })
	scalar("current_database", 0, func([]driver.Value) (driver.Value, error) { return catalogDBName, nil })
	scalar("current_user", 0, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("session_user", 0, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("pg_get_userbyid", 1, func([]driver.Value) (driver.Value, error) { return catalogRole, nil })
	scalar("pg_table_is_visible", 1, func([]driver.Value) (driver.Value, error) { return int64(1), nil })
	scalar("pg_get_expr", -1, func([]driver.Value) (driver.Value, error) { return "", nil })

	// Definition-rendering helpers psql calls in \d; we don't reconstruct DDL
	// yet, so they return empty strings (variadic arg counts via nArg -1).
	for _, name := range []string{
		"pg_get_indexdef", "pg_get_constraintdef", "pg_get_viewdef",
		"pg_get_triggerdef", "pg_get_partkeydef", "pg_get_ruledef",
		"pg_get_function_identity_arguments", "pg_get_functiondef",
	} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return "", nil })
	}
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
	for _, name := range []string{"array_upper", "array_lower", "array_length", "array_ndims"} {
		scalar(name, -1, func([]driver.Value) (driver.Value, error) { return nil, nil })
	}

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

	// Date/time functions (now()/extract are handled in the dialect layer).
	scalar("date_trunc", 2, dateTruncFn)
	scalar("date_part", 2, datePartFn)
	scalar("to_char", 2, toCharFn)

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
	 SELECT ov_t.*, NULL AS typacl, 0 AS typispreferred, 1 AS typisdefined,
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
	 ) ov_t`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_enum AS
	 SELECT CAST(e.rowid AS INTEGER) AS oid,
	        CAST((SELECT t.rowid + 90000000 FROM _overlite_enum_types t WHERE t.typname = e.typname) AS INTEGER) AS enumtypid,
	        CAST(e.sortorder AS REAL) AS enumsortorder, e.label AS enumlabel
	 FROM _overlite_enums e`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_auth_members AS
	 SELECT 0 AS oid, 0 AS roleid, 0 AS member, 0 AS grantor, 0 AS admin_option
	 WHERE 0`,

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

	`CREATE TEMP VIEW IF NOT EXISTS pg_attrdef AS
	 SELECT 0 AS oid, 0 AS adrelid, 0 AS adnum, NULL AS adbin
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_description AS
	 SELECT 0 AS objoid, 0 AS classoid, 0 AS objsubid, '' AS description
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_inherits AS
	 SELECT 0 AS inhrelid, 0 AS inhparent, 0 AS inhseqno, 0 AS inhdetachpending
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_trigger AS
	 SELECT 0 AS oid, 0 AS tgrelid, '' AS tgname, 0 AS tgfoid, 0 AS tgtype,
	        'O' AS tgenabled, 0 AS tgisinternal, 0 AS tgconstrrelid,
	        0 AS tgconstrindid, 0 AS tgconstraint, 0 AS tgdeferrable,
	        0 AS tginitdeferred, 0 AS tgnargs, '' AS tgattr, '' AS tgargs,
	        NULL AS tgqual, NULL AS tgoldtable, NULL AS tgnewtable
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_rewrite AS
	 SELECT 0 AS oid, '' AS rulename, 0 AS ev_class, '1' AS ev_type,
	        'O' AS ev_enabled, 0 AS is_instead, NULL AS ev_qual, NULL AS ev_action
	 WHERE 0`,

	`CREATE TEMP VIEW IF NOT EXISTS pg_policy AS
	 SELECT 0 AS oid, '' AS polname, 0 AS polrelid, '*' AS polcmd,
	        1 AS polpermissive, NULL AS polroles, NULL AS polqual, NULL AS polwithcheck
	 WHERE 0`,

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
	`CREATE TEMP VIEW IF NOT EXISTS pg_depend AS
	 SELECT 0 AS classid, 0 AS objid, 0 AS objsubid, 0 AS refclassid, 0 AS refobjid,
	        0 AS refobjsubid, 'n' AS deptype
	 WHERE 0`,
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
	 SELECT 0 AS oid, '' AS evtname, '' AS evtevent, 10 AS evtowner WHERE 0`,

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

	// No user-defined functions yet; empty so \df executes.
	`CREATE TEMP VIEW IF NOT EXISTS pg_proc AS
	 SELECT 0 AS oid, '' AS proname, 11 AS pronamespace, 10 AS proowner,
	        0 AS prolang, 'f' AS prokind, 0 AS prorettype, '' AS proargtypes,
	        0 AS pronargs, 0 AS proretset
	 WHERE 0`,
}
