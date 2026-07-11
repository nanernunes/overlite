package engine

import (
	"strconv"
	"strings"
)

// frag instantiates a per-schema SQL fragment: @DB@ = attached db name, @NS@ =
// namespace oid, @OFF@ = oid offset (keeps ids distinct across schemas), @PG@ =
// the Postgres schema name.
func frag(tmpl string, r schemaRef) string {
	return strings.NewReplacer(
		"@DB@", r.DB,
		"@NS@", strconv.Itoa(r.NsOid),
		"@OFF@", strconv.FormatInt(r.Offset, 10),
		"@PG@", r.PgName,
	).Replace(tmpl)
}

func union(name string, refs []schemaRef, tmpl string) string {
	parts := make([]string, len(refs))
	for i, r := range refs {
		parts[i] = frag(tmpl, r)
	}
	return "CREATE TEMP VIEW " + name + " AS\n" + strings.Join(parts, "\nUNION ALL\n")
}

// dynamicCatalogViews returns the catalog views that span every attached
// schema. Rebuilt whenever the set of schemas changes.
func dynamicCatalogViews(refs []schemaRef) []string {
	// pg_namespace: fixed system schemas plus one row per real schema.
	ns := "CREATE TEMP VIEW pg_namespace AS" +
		" SELECT 11 AS oid,'pg_catalog' AS nspname,10 AS nspowner" +
		" UNION ALL SELECT 2201,'information_schema',10"
	for _, r := range refs {
		ns += " UNION ALL SELECT " + strconv.Itoa(r.NsOid) + ",'" + r.PgName + "',10"
	}

	return []string{
		ns,
		pgClassView(refs),
		union("pg_attribute", refs, pgAttributeTmpl),
		union("pg_index", refs, pgIndexTmpl),
		union("pg_constraint", refs, pgConstraintTmpl),
		union(`"information_schema.tables"`, refs, infoTablesTmpl),
		union(`"information_schema.columns"`, refs, infoColumnsTmpl),
	}
}

// pgClassView is pg_class over every schema, plus the sequences (relkind 'S')
// that live in the main/public database, so \ds and \d <seq> find them.
func pgClassView(refs []schemaRef) string {
	parts := make([]string, 0, len(refs)+1)
	for _, r := range refs {
		parts = append(parts, frag(pgClassTmpl, r))
	}
	for _, r := range refs {
		if r.DB == "main" {
			parts = append(parts, frag(pgSequenceClassTmpl, r))
		}
	}
	return "CREATE TEMP VIEW pg_class AS\n" + strings.Join(parts, "\nUNION ALL\n")
}

// pgSequenceClassTmpl adds one pg_class row per sequence (column order matches
// pgClassTmpl; names come from the first SELECT in the UNION).
const pgSequenceClassTmpl = `SELECT CAST(seq.rowid + 80000000 + @OFF@ AS INTEGER) AS oid, seq.seqname AS relname, @NS@ AS relnamespace,
 0,0,10,0,0,0,0,0,0,0,
 0, 0, 'p', 'S',
 1, 0,0,0,0,0,0,1,'d',0,NULL,NULL,NULL,0,0
FROM _overlite_sequences seq`

const pgClassTmpl = `SELECT CAST(m.rowid + @OFF@ AS INTEGER) AS oid, m.name AS relname, @NS@ AS relnamespace,
 0 AS reltype, 0 AS reloftype, 10 AS relowner, 0 AS relam, 0 AS relfilenode, 0 AS reltablespace,
 0 AS relpages, 0 AS reltuples, 0 AS relallvisible, 0 AS reltoastrelid,
 CASE WHEN m.type='table' AND (EXISTS(SELECT 1 FROM pragma_index_list(m.name,'@DB@'))
       OR EXISTS(SELECT 1 FROM pragma_table_info(m.name,'@DB@') WHERE pk>0)) THEN 1 ELSE 0 END AS relhasindex,
 0 AS relisshared, 'p' AS relpersistence,
 CASE m.type WHEN 'view' THEN 'v' WHEN 'index' THEN 'i' ELSE 'r' END AS relkind,
 (SELECT count(*) FROM pragma_table_info(m.name,'@DB@')) AS relnatts,
 0 AS relchecks, 0 AS relhasrules, 0 AS relhastriggers, 0 AS relhassubclass, 0 AS relrowsecurity,
 0 AS relforcerowsecurity, 1 AS relispopulated, 'd' AS relreplident, 0 AS relispartition,
 NULL AS relacl, NULL AS reloptions, NULL AS relpartbound, 0 AS relrewrite, 0 AS relminmxid
FROM @DB@.sqlite_master m
WHERE m.type IN ('table','view','index') AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'
UNION ALL
SELECT CAST(tbl.rowid + 90000000 + @OFF@ AS INTEGER), tbl.name || '_pkey', @NS@, 0,0,10,403,0,0,0,0,0,0,0,0,'p','i',
 (SELECT count(*) FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0),
 0,0,0,0,0,0,1,'n',0,NULL,NULL,NULL,0,0
FROM @DB@.sqlite_master tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)`

const pgAttributeTmpl = `SELECT CAST(m.rowid + @OFF@ AS INTEGER) AS attrelid, ti.name AS attname, overlite_type_oid(ti.type) AS atttypid,
 -1 AS attstattarget, -1 AS attlen, ti.cid + 1 AS attnum, 0 AS attndims, -1 AS attcacheoff, -1 AS atttypmod,
 0 AS attbyval, 'p' AS attstorage, 'i' AS attalign,
 CASE WHEN ti."notnull"=1 OR ti.pk>0 THEN 1 ELSE 0 END AS attnotnull,
 CASE WHEN ti.dflt_value IS NOT NULL THEN 1 ELSE 0 END AS atthasdef,
 0 AS atthasmissing, '' AS attidentity, '' AS attgenerated, 0 AS attisdropped, 1 AS attislocal,
 0 AS attinhcount, 0 AS attcollation, NULL AS attacl, NULL AS attoptions
FROM @DB@.sqlite_master m JOIN pragma_table_info(m.name,'@DB@') ti
WHERE m.type IN ('table','view') AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'`

const pgIndexTmpl = `SELECT idx.rowid + @OFF@ AS indexrelid, tbl.rowid + @OFF@ AS indrelid,
 (SELECT count(*) FROM pragma_index_info(idx.name,'@DB@')) AS indnatts,
 (SELECT count(*) FROM pragma_index_info(idx.name,'@DB@')) AS indnkeyatts,
 il."unique" AS indisunique, CASE il.origin WHEN 'pk' THEN 1 ELSE 0 END AS indisprimary,
 0 AS indisexclusion, 1 AS indimmediate, 0 AS indisclustered, 1 AS indisvalid, 0 AS indcheckxmin,
 1 AS indisready, 1 AS indislive, 0 AS indisreplident,
 (SELECT group_concat(ii.cid+1,' ') FROM pragma_index_info(idx.name,'@DB@') ii) AS indkey,
 '' AS indcollation, '' AS indclass, '' AS indoption, NULL AS indexprs, NULL AS indpred,
 (SELECT group_concat(ii.name,', ') FROM pragma_index_info(idx.name,'@DB@') ii) AS ov_cols
FROM @DB@.sqlite_master idx
JOIN @DB@.sqlite_master tbl ON tbl.name = idx.tbl_name AND tbl.type='table'
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
FROM @DB@.sqlite_master tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)`

const pgConstraintTmpl = `SELECT tbl.rowid*100000 + fk.id + @OFF@ AS oid, 'fk_' || tbl.name || '_' || fk.id AS conname,
 @NS@ AS connamespace, 'f' AS contype, 0 AS condeferrable, 0 AS condeferred, 1 AS convalidated,
 CAST(tbl.rowid + @OFF@ AS INTEGER) AS conrelid, 0 AS contypid, 0 AS conindid, 0 AS conparentid,
 CAST((SELECT rowid + @OFF@ FROM @DB@.sqlite_master WHERE name = fk."table" AND type='table') AS INTEGER) AS confrelid,
 'a' AS confupdtype, 'a' AS confdeltype, 's' AS confmatchtype, 1 AS conislocal, 0 AS coninhcount,
 1 AS connoinherit, '' AS conkey, '' AS confkey, NULL AS conbin,
 fk."from" AS ov_cols, fk."table" || '(' || fk."to" || ')' AS ov_ref
FROM @DB@.sqlite_master tbl JOIN pragma_foreign_key_list(tbl.name,'@DB@') fk
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
UNION ALL
SELECT tbl.rowid*100000 + 99999 + @OFF@, tbl.name || '_pkey', @NS@, 'p', 0,0,1,
 CAST(tbl.rowid + @OFF@ AS INTEGER), 0,0,0,0, ' ',' ',' ', 1,0,1, '','',NULL,
 (SELECT group_concat(name,', ') FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0 ORDER BY pk), NULL
FROM @DB@.sqlite_master tbl
WHERE tbl.type='table' AND tbl.name NOT LIKE 'sqlite_%' AND tbl.name NOT GLOB '_overlite_*'
  AND EXISTS(SELECT 1 FROM pragma_table_info(tbl.name,'@DB@') WHERE pk>0)`

const infoTablesTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema, name AS table_name,
 CASE type WHEN 'view' THEN 'VIEW' ELSE 'BASE TABLE' END AS table_type
FROM @DB@.sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%' AND name NOT GLOB '_overlite_*'`

const infoColumnsTmpl = `SELECT 'main' AS table_catalog, '@PG@' AS table_schema, m.name AS table_name,
 ti.name AS column_name, ti.cid + 1 AS ordinal_position, ti.type AS data_type,
 CASE ti."notnull" WHEN 1 THEN 'NO' ELSE 'YES' END AS is_nullable
FROM @DB@.sqlite_master m JOIN pragma_table_info(m.name,'@DB@') ti
WHERE m.type='table' AND m.name NOT LIKE 'sqlite_%' AND m.name NOT GLOB '_overlite_*'`
