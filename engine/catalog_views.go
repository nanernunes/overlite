package engine

import (
	"regexp"
	"strconv"
	"strings"
)

// frag instantiates a per-schema SQL fragment: @DB@ = attached db name (pragma
// scope), @MASTER@ = the schema's table source (an sqlite_master or a filtered
// view of it), @NS@ = namespace oid, @OFF@ = oid offset (keeps ids distinct
// across schemas), @PG@ = the Postgres schema name.
func frag(tmpl string, r schemaRef) string {
	return strings.NewReplacer(
		"@DB@", r.DB,
		"@MASTER@", r.master(),
		"@NS@", strconv.Itoa(r.NsOid),
		"@OFF@", strconv.FormatInt(r.Offset, 10),
		"@PG@", r.PgName,
		// @PLEN@ strips a single-file schema's "vendas." table-name prefix from
		// displayed relation names; it's 1 (a no-op, substr(name,1)=name) for
		// public and for multi-file schemas.
		"@PLEN@", strconv.Itoa(len(r.Prefix)+1),
	).Replace(tmpl)
}

func union(name string, refs []schemaRef, tmpl string) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = frag(tmpl, r)
	}
	return "CREATE TEMP VIEW " + name + " AS\n" + strings.Join(parts, "\nUNION ALL\n")
}

// reViewHeader captures the "CREATE TEMP VIEW <name> AS " prefix and the body.
var reViewHeader = regexp.MustCompile(`(?is)^(\s*CREATE\s+TEMP\s+VIEW\s+(?:IF\s+NOT\s+EXISTS\s+)?(?:"[^"]+"|\w+)\s+AS\s+)(.*)$`)

// withTableOID wraps a "CREATE TEMP VIEW ... AS <body>" so the view exposes a
// tableoid column (pg_dump selects it from every catalog). Views that already
// define one (e.g. pg_type) are left untouched.
func withTableOID(stmt string) string {
	m := reViewHeader.FindStringSubmatch(stmt)
	if m == nil || strings.Contains(strings.ToLower(m[2]), "tableoid") {
		return stmt
	}
	return m[1] + "SELECT _s.*, 0 AS tableoid FROM (" + m[2] + ") _s"
}

// unionWrap wraps union() so the view exposes extraCols (constant columns
// pg_dump selects that aren't worth threading through every UNION branch).
func unionWrap(name, extraCols string, refs []schemaRef, tmpl string) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = frag(tmpl, r)
	}
	return "CREATE TEMP VIEW " + name + " AS SELECT _u.*, " + extraCols +
		" FROM (\n" + strings.Join(parts, "\nUNION ALL\n") + "\n) _u"
}

// unionOID is unionWrap with just a constant tableoid column (the catalog's own
// oid in pg_class), a row's originating-catalog discriminator for pg_dump.
func unionOID(name string, tableOID int, refs []schemaRef, tmpl string) string {
	return unionWrap(name, strconv.Itoa(tableOID)+" AS tableoid", refs, tmpl)
}

// dynamicCatalogViews returns the catalog views that span every attached
// schema. Rebuilt whenever the set of schemas changes.
func dynamicCatalogViews(refs []schemaRef) []string {
	// pg_namespace: fixed system schemas plus one row per real schema.
	ns := "CREATE TEMP VIEW pg_namespace AS" +
		" SELECT 11 AS oid,'pg_catalog' AS nspname,10 AS nspowner, 2615 AS tableoid, NULL AS nspacl" +
		" UNION ALL SELECT 2201,'information_schema',10,2615,NULL"
	for _, r := range refs {
		ns += " UNION ALL SELECT " + strconv.Itoa(r.NsOid) + ",'" + r.PgName + "',10,2615,NULL"
	}

	return []string{
		ns,
		pgClassView(refs),
		unionOID("pg_attribute", 1249, refs, pgAttributeTmpl),
		unionOID("pg_attrdef", 2604, refs, pgAttrdefTmpl),
		unionWrap("pg_index", "2610 AS tableoid, 0 AS indnullsnotdistinct", refs, pgIndexTmpl),
		unionWrap("pg_constraint", "2606 AS tableoid, NULL AS conparentid2, 0 AS conperiod", refs, pgConstraintTmpl),
		unionWrap("pg_trigger", "2620 AS tableoid, 0 AS tgparentid", refs, pgTriggerTmpl),
		union(`"information_schema.tables"`, refs, infoTablesTmpl),
		union(`"information_schema.columns"`, refs, infoColumnsTmpl),
		union(`"information_schema.table_constraints"`, refs, infoTableConstraintsTmpl),
		union(`"information_schema.key_column_usage"`, refs, infoKeyColumnUsageTmpl),
		union(`"information_schema.referential_constraints"`, refs, infoReferentialConstraintsTmpl),
		union(`"information_schema.constraint_column_usage"`, refs, infoConstraintColumnUsageTmpl),
		union(`"information_schema.views"`, refs, infoViewsTmpl),
		infoSchemataView(refs),
		infoSequencesView(),
		infoCheckConstraintsView(),
		infoRoutinesView(),
		pgProcView(),
	}
}

// infoSchemataView lists every schema (system + user) as information_schema.schemata.
func infoSchemataView(refs []schemaRef) string {
	v := `CREATE TEMP VIEW "information_schema.schemata" AS` +
		` SELECT ` + sqlQuote(catalogDBName) + ` AS catalog_name, 'information_schema' AS schema_name, ` +
		sqlQuote(catalogRole) + ` AS schema_owner` +
		` UNION ALL SELECT ` + sqlQuote(catalogDBName) + `,'pg_catalog',` + sqlQuote(catalogRole)
	for _, r := range refs {
		v += ` UNION ALL SELECT ` + sqlQuote(catalogDBName) + `,'` + r.PgName + `',` + sqlQuote(catalogRole)
	}
	return v
}

// infoSequencesView exposes the internal sequence table as information_schema.sequences.
func infoSequencesView() string {
	return `CREATE TEMP VIEW "information_schema.sequences" AS
	 SELECT ` + sqlQuote(catalogDBName) + ` AS sequence_catalog, 'public' AS sequence_schema,
	        seqname AS sequence_name, 'bigint' AS data_type, 64 AS numeric_precision,
	        2 AS numeric_precision_radix, 0 AS numeric_scale,
	        CAST(start_value AS TEXT) AS start_value, CAST(min_value AS TEXT) AS minimum_value,
	        CAST(max_value AS TEXT) AS maximum_value, CAST(increment AS TEXT) AS increment,
	        CASE is_cycled WHEN 1 THEN 'YES' ELSE 'NO' END AS cycle_option
	 FROM _overlite_sequences`
}

// infoCheckConstraintsView is present but empty (we don't enumerate CHECK
// clauses yet). infoRoutinesView is defined in catalog.go from the function list.
func infoCheckConstraintsView() string {
	return `CREATE TEMP VIEW "information_schema.check_constraints" AS
	 SELECT ` + sqlQuote(catalogDBName) + ` AS constraint_catalog, 'public' AS constraint_schema,
	        '' AS constraint_name, '' AS check_clause WHERE 0`
}

// pgTriggerTmpl exposes each schema's SQLite triggers (sqlite_master type
// 'trigger') as pg_trigger rows, with tgrelid pointing at the owning table's
// pg_class oid so psql's \d lists them.
const pgTriggerTmpl = `SELECT CAST(trg.rowid + 70000000 + @OFF@ AS INTEGER) AS oid,
 CAST(tbl.rowid + @OFF@ AS INTEGER) AS tgrelid, trg.name AS tgname, 0 AS tgfoid, 0 AS tgtype,
 'O' AS tgenabled, 0 AS tgisinternal, 0 AS tgconstrrelid, 0 AS tgconstrindid, 0 AS tgconstraint,
 0 AS tgdeferrable, 0 AS tginitdeferred, 0 AS tgnargs, '' AS tgattr, '' AS tgargs,
 NULL AS tgqual, NULL AS tgoldtable, NULL AS tgnewtable
FROM @MASTER@ trg
JOIN @MASTER@ tbl ON tbl.name = trg.tbl_name AND tbl.type = 'table'
WHERE trg.type = 'trigger' AND trg.name NOT LIKE 'sqlite_%' AND trg.name NOT GLOB '_overlite_*'`

// pgClassView is pg_class over every schema, plus the sequences (relkind 'S')
// that live in the main/public database, so \ds and \d <seq> find them.
func pgClassView(refs []schemaRef) string {
	parts := make([]string, 0, len(refs)+1)
	for _, r := range refs {
		parts = append(parts, frag(pgClassTmpl, r))
	}
	for _, r := range refs {
		parts = append(parts, frag(pgUniqueIndexClassTmpl, r))
	}
	for _, r := range refs {
		if r.PgName == "public" {
			parts = append(parts, frag(pgSequenceClassTmpl, r))
		}
	}
	return "CREATE TEMP VIEW pg_class AS SELECT _c.*, 1259 AS tableoid," +
		" 0 AS relfrozenxid, 0 AS relhasoids, NULL AS relpartbound2, 0 AS relallfrozen FROM (\n" +
		strings.Join(parts, "\nUNION ALL\n") + "\n) _c"
}

// pgUniqueIndexClassTmpl adds a pg_class row (relkind 'i') for each UNIQUE
// constraint's SQLite auto-index (which lives in pragma_index_list, not
// sqlite_master), so pg_dump can resolve the constraint's backing index.
const pgUniqueIndexClassTmpl = `SELECT CAST(tbl.rowid*1000 + il.seq + 84000000 + @OFF@ AS INTEGER) AS oid,
 substr(tbl.name,@PLEN@) || '_' || (SELECT ii.name FROM pragma_index_info(il.name,'@DB@') ii WHERE ii.seqno=0) || '_key' AS relname,
 @NS@ AS relnamespace,
 0,0,10,403,0,0,0,0,0,0,
 0, 0, 'p', 'i',
 (SELECT count(*) FROM pragma_index_info(il.name,'@DB@')), 0,0,0,0,0,0,1,'n',0,NULL,NULL,NULL,0,0
FROM @MASTER@ tbl JOIN pragma_index_list(tbl.name,'@DB@') il
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND il.origin='u' AND il."unique"=1`

// pgSequenceClassTmpl adds one pg_class row per sequence (column order matches
// pgClassTmpl; names come from the first SELECT in the UNION).
const pgSequenceClassTmpl = `SELECT CAST(seq.rowid + 80000000 + @OFF@ AS INTEGER) AS oid, seq.seqname AS relname, @NS@ AS relnamespace,
 0,0,10,0,0,0,0,0,0,0,
 0, 0, 'p', 'S',
 1, 0,0,0,0,0,0,1,'d',0,NULL,NULL,NULL,0,0
FROM _overlite_sequences seq`

const pgClassTmpl = `SELECT CAST(m.rowid + @OFF@ AS INTEGER) AS oid, substr(m.name,@PLEN@) AS relname, @NS@ AS relnamespace,
 0 AS reltype, 0 AS reloftype, 10 AS relowner, 0 AS relam, 0 AS relfilenode, 0 AS reltablespace,
 0 AS relpages, 0 AS reltuples, 0 AS relallvisible, 0 AS reltoastrelid,
 CASE WHEN m.type='table' AND (EXISTS(SELECT 1 FROM pragma_index_list(m.name,'@DB@'))
       OR EXISTS(SELECT 1 FROM pragma_table_info(m.name,'@DB@') WHERE pk>0)) THEN 1 ELSE 0 END AS relhasindex,
 0 AS relisshared, 'p' AS relpersistence,
 CASE m.type WHEN 'view' THEN 'v' WHEN 'index' THEN 'i' ELSE 'r' END AS relkind,
 (SELECT count(*) FROM pragma_table_info(m.name,'@DB@')) AS relnatts,
 0 AS relchecks, 0 AS relhasrules,
 CASE WHEN EXISTS(SELECT 1 FROM @MASTER@ trg WHERE trg.type='trigger' AND trg.tbl_name=m.name)
      THEN 1 ELSE 0 END AS relhastriggers,
 0 AS relhassubclass,
 COALESCE((SELECT enabled FROM _overlite_rls rls WHERE lower(rls.tablename)=lower(m.name)),0) AS relrowsecurity,
 COALESCE((SELECT forced FROM _overlite_rls rls WHERE lower(rls.tablename)=lower(m.name)),0) AS relforcerowsecurity,
 1 AS relispopulated, 'd' AS relreplident, 0 AS relispartition,
 NULL AS relacl, NULL AS reloptions, NULL AS relpartbound, 0 AS relrewrite, 0 AS relminmxid
FROM @MASTER@ m
WHERE m.type IN ('table','view','index') AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'
UNION ALL
SELECT CAST(tbl.rowid + 90000000 + @OFF@ AS INTEGER), substr(tbl.name,@PLEN@) || '_pkey', @NS@, 0,0,10,403,0,0,0,0,0,0,0,0,'p','i',
 (SELECT count(*) FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0),
 0,0,0,0,0,0,1,'n',0,NULL,NULL,NULL,0,0
FROM @MASTER@ tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)`

const pgAttributeTmpl = `SELECT CAST(m.rowid + @OFF@ AS INTEGER) AS attrelid, ti.name AS attname, overlite_type_oid(ti.type) AS atttypid,
 -1 AS attstattarget, -1 AS attlen, ti.cid + 1 AS attnum, 0 AS attndims, -1 AS attcacheoff, -1 AS atttypmod,
 0 AS attbyval, 'p' AS attstorage, 'i' AS attalign,
 CASE WHEN ti."notnull"=1 OR ti.pk>0 THEN 1 ELSE 0 END AS attnotnull,
 CASE WHEN ti.dflt_value IS NOT NULL THEN 1 ELSE 0 END AS atthasdef,
 0 AS atthasmissing, '' AS attidentity, '' AS attgenerated, 0 AS attisdropped, 1 AS attislocal,
 0 AS attinhcount, 0 AS attcollation, NULL AS attacl, NULL AS attoptions,
 '' AS attcompression, NULL AS attmissingval, 0 AS attispartkey, 0 AS attstorage2
FROM @MASTER@ m JOIN pragma_table_info(m.name,'@DB@') ti
WHERE m.type IN ('table','view') AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'`

// pgAttrdefTmpl exposes column defaults (adbin holds the default's SQL text,
// which pg_get_expr returns verbatim).
const pgAttrdefTmpl = `SELECT CAST(m.rowid * 1000 + ti.cid + @OFF@ AS INTEGER) AS oid,
 CAST(m.rowid + @OFF@ AS INTEGER) AS adrelid, ti.cid + 1 AS adnum, ti.dflt_value AS adbin
FROM @MASTER@ m JOIN pragma_table_info(m.name,'@DB@') ti
WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'
  AND ti.dflt_value IS NOT NULL`

const pgIndexTmpl = `SELECT idx.rowid + @OFF@ AS indexrelid, tbl.rowid + @OFF@ AS indrelid,
 (SELECT count(*) FROM pragma_index_info(idx.name,'@DB@')) AS indnatts,
 (SELECT count(*) FROM pragma_index_info(idx.name,'@DB@')) AS indnkeyatts,
 il."unique" AS indisunique, CASE il.origin WHEN 'pk' THEN 1 ELSE 0 END AS indisprimary,
 0 AS indisexclusion, 1 AS indimmediate, 0 AS indisclustered, 1 AS indisvalid, 0 AS indcheckxmin,
 1 AS indisready, 1 AS indislive, 0 AS indisreplident,
 (SELECT group_concat(ii.cid+1,' ') FROM pragma_index_info(idx.name,'@DB@') ii) AS indkey,
 '' AS indcollation, '' AS indclass, '' AS indoption, NULL AS indexprs, NULL AS indpred,
 (SELECT group_concat(ii.name,', ') FROM pragma_index_info(idx.name,'@DB@') ii) AS ov_cols
FROM @MASTER@ idx
JOIN @MASTER@ tbl ON tbl.name = idx.tbl_name AND tbl.type='table'
JOIN pragma_index_list(tbl.name,'@DB@') il ON il.name = idx.name
WHERE idx.type='index' AND idx.name NOT LIKE 'sqlite_%' AND idx.name NOT GLOB '_overlite_*'
UNION ALL
SELECT tbl.rowid + 90000000 + @OFF@, tbl.rowid + @OFF@,
 (SELECT count(*) FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0),
 (SELECT count(*) FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0),
 1,1,0,1,0,1,0,1,1,0,
 (SELECT group_concat(cid+1,' ') FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0 ORDER BY pk),
 '','','',NULL,NULL,
 (SELECT group_concat(name,', ') FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0 ORDER BY pk)
FROM @MASTER@ tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)
UNION ALL
SELECT CAST(tbl.rowid*1000 + il.seq + 84000000 + @OFF@ AS INTEGER), tbl.rowid + @OFF@,
 (SELECT count(*) FROM pragma_index_info(il.name,'@DB@')),
 (SELECT count(*) FROM pragma_index_info(il.name,'@DB@')),
 1,0,0,1,0,1,0,1,1,0,
 (SELECT group_concat(ii.cid+1,' ') FROM pragma_index_info(il.name,'@DB@') ii),
 '','','',NULL,NULL,
 (SELECT group_concat(ii.name,', ') FROM pragma_index_info(il.name,'@DB@') ii)
FROM @MASTER@ tbl JOIN pragma_index_list(tbl.name,'@DB@') il
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND il.origin='u' AND il."unique"=1`

const pgConstraintTmpl = `SELECT tbl.rowid*100000 + fk.id + @OFF@ AS oid, 'fk_' || tbl.name || '_' || fk.id AS conname,
 @NS@ AS connamespace, 'f' AS contype, 0 AS condeferrable, 0 AS condeferred, 1 AS convalidated,
 CAST(tbl.rowid + @OFF@ AS INTEGER) AS conrelid, 0 AS contypid, 0 AS conindid, 0 AS conparentid,
 CAST((SELECT rowid + @OFF@ FROM @MASTER@ WHERE name = fk."table" AND type='table') AS INTEGER) AS confrelid,
 'a' AS confupdtype, 'a' AS confdeltype, 's' AS confmatchtype, 1 AS conislocal, 0 AS coninhcount,
 1 AS connoinherit, '' AS conkey, '' AS confkey, NULL AS conbin,
 fk."from" AS ov_cols, fk."table" || '(' || fk."to" || ')' AS ov_ref
FROM @MASTER@ tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
UNION ALL
SELECT tbl.rowid*100000 + 99999 + @OFF@, tbl.name || '_pkey', @NS@, 'p', 0,0,1,
 CAST(tbl.rowid + @OFF@ AS INTEGER), 0, CAST(tbl.rowid + 90000000 + @OFF@ AS INTEGER), 0, 0,
 ' ',' ',' ', 1,0,1, '','',NULL,
 (SELECT group_concat(name,', ') FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0 ORDER BY pk), NULL
FROM @MASTER@ tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)
UNION ALL
SELECT tbl.rowid*100000 + 88000 + il.seq + @OFF@,
 tbl.name || '_' || (SELECT ii.name FROM pragma_index_info(il.name,'@DB@') ii WHERE ii.seqno=0) || '_key', @NS@, 'u', 0,0,1,
 CAST(tbl.rowid + @OFF@ AS INTEGER), 0, CAST(tbl.rowid*1000 + il.seq + 84000000 + @OFF@ AS INTEGER), 0, 0,
 ' ',' ',' ', 1,0,1, '','',NULL,
 (SELECT group_concat(ii.name,', ') FROM pragma_index_info(il.name,'@DB@') ii), NULL
FROM @MASTER@ tbl JOIN pragma_index_list(tbl.name,'@DB@') il
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND il.origin='u' AND il."unique"=1`

const infoTablesTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema, substr(name,@PLEN@) AS table_name,
 CASE type WHEN 'view' THEN 'VIEW' ELSE 'BASE TABLE' END AS table_type
FROM @MASTER@ WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' AND name NOT GLOB '_overlite_*'`

const infoColumnsTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema, substr(m.name,@PLEN@) AS table_name,
 ti.name AS column_name, ti.cid + 1 AS ordinal_position, ti.dflt_value AS column_default,
 CASE WHEN ti."notnull"=1 OR ti.pk>0 THEN 'NO' ELSE 'YES' END AS is_nullable,
 format_type(overlite_type_oid(ti.type), NULL) AS data_type,
 overlite_char_max_length(ti.type) AS character_maximum_length, NULL AS character_octet_length,
 overlite_numeric_precision(ti.type) AS numeric_precision,
 CASE WHEN overlite_numeric_precision(ti.type) IS NOT NULL THEN 10 END AS numeric_precision_radix,
 overlite_numeric_scale(ti.type) AS numeric_scale,
 NULL AS datetime_precision,
 'main' AS udt_catalog, 'pg_catalog' AS udt_schema,
 format_type(overlite_type_oid(ti.type), NULL) AS udt_name,
 NULL AS collation_name, NULL AS domain_catalog, NULL AS domain_schema, NULL AS domain_name,
 ti.cid + 1 AS dtd_identifier, 'NO' AS is_identity, 'NO' AS is_generated,
 'NEVER' AS identity_generation, 'YES' AS is_updatable
FROM @MASTER@ m JOIN pragma_table_info(m.name,'@DB@') ti
WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'`

// The constraint-family views are derived from SQLite pragmas. Constraint names
// match pg_constraint: "<table>_pkey" for the primary key, "fk_<table>_<id>" for
// a foreign key, and the index name for a UNIQUE constraint.

const infoTableConstraintsTmpl = `SELECT 'main' AS constraint_catalog, '@PG@' AS constraint_schema,
 tbl.name || '_pkey' AS constraint_name, 'main' AS table_catalog, '@PG@' AS table_schema,
 tbl.name AS table_name, 'PRIMARY KEY' AS constraint_type, 'NO' AS is_deferrable,
 'NO' AS initially_deferred, 'YES' AS enforced
FROM @MASTER@ tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)
UNION ALL
SELECT 'main', '@PG@', 'fk_' || tbl.name || '_' || fk.id, 'main', '@PG@', tbl.name,
 'FOREIGN KEY', 'NO', 'NO', 'YES'
FROM @MASTER@ tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*' AND fk.seq=0
UNION ALL
SELECT 'main', '@PG@',
 tbl.name || '_' || (SELECT x.name FROM pragma_index_info(il.name,'@DB@') x WHERE x.seqno=0) || '_key',
 'main', '@PG@', tbl.name, 'UNIQUE', 'NO', 'NO', 'YES'
FROM @MASTER@ tbl JOIN pragma_index_list(tbl.name,'@DB@') il
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND il.origin='u' AND il."unique"=1`

const infoKeyColumnUsageTmpl = `SELECT 'main' AS constraint_catalog, '@PG@' AS constraint_schema,
 tbl.name || '_pkey' AS constraint_name, 'main' AS table_catalog, '@PG@' AS table_schema,
 tbl.name AS table_name, ti.name AS column_name, ti.pk AS ordinal_position,
 NULL AS position_in_unique_constraint
FROM @MASTER@ tbl JOIN pragma_table_info(tbl.name,'@DB@') ti
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*' AND ti.pk>0
UNION ALL
SELECT 'main', '@PG@', 'fk_' || tbl.name || '_' || fk.id, 'main', '@PG@', tbl.name,
 fk."from", fk.seq+1, fk.seq+1
FROM @MASTER@ tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
UNION ALL
SELECT 'main', '@PG@',
 tbl.name || '_' || (SELECT x.name FROM pragma_index_info(il.name,'@DB@') x WHERE x.seqno=0) || '_key',
 'main', '@PG@', tbl.name, ii.name, ii.seqno+1, NULL
FROM @MASTER@ tbl JOIN pragma_index_list(tbl.name,'@DB@') il
 JOIN pragma_index_info(il.name,'@DB@') ii
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND il.origin='u' AND il."unique"=1`

const infoReferentialConstraintsTmpl = `SELECT 'main' AS constraint_catalog, '@PG@' AS constraint_schema,
 'fk_' || tbl.name || '_' || fk.id AS constraint_name, 'main' AS unique_constraint_catalog,
 '@PG@' AS unique_constraint_schema, fk."table" || '_pkey' AS unique_constraint_name,
 'NONE' AS match_option, fk.on_update AS update_rule, fk.on_delete AS delete_rule
FROM @MASTER@ tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*' AND fk.seq=0`

const infoConstraintColumnUsageTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema,
 tbl.name AS table_name, ti.name AS column_name, 'main' AS constraint_catalog,
 '@PG@' AS constraint_schema, tbl.name || '_pkey' AS constraint_name
FROM @MASTER@ tbl JOIN pragma_table_info(tbl.name,'@DB@') ti
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*' AND ti.pk>0
UNION ALL
SELECT 'main', '@PG@', fk."table", fk."to", 'main', '@PG@', 'fk_' || tbl.name || '_' || fk.id
FROM @MASTER@ tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'`

const infoViewsTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema, name AS table_name,
 sql AS view_definition, 'NONE' AS check_option, 'NO' AS is_updatable, 'NO' AS is_insertable_into,
 'NO' AS is_trigger_updatable, 'NO' AS is_trigger_deletable, 'NO' AS is_trigger_insertable_into
FROM @MASTER@ WHERE type='view' AND name NOT LIKE 'sqlite_%' AND name NOT GLOB '_overlite_*'`
