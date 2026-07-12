package engine

// COMMENT ON stores object comments that SQLite has no place for. They live in
// `_overlite_comments` (main database, persistent, hidden by the `_overlite_*`
// GLOB filter) and are surfaced through the `pg_description` view, which the
// obj_description()/col_description() rewrites and psql's \d+ read.
//
// One row per commented object. objkind is 'table'/'view'/'index'/'sequence'/
// 'column'/... ; objname is the relation (or type/schema) name; subname is the
// column name for column comments (empty otherwise). A NULL/removed comment is
// stored as a deletion, so the table only ever holds live comments.
const commentsTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_comments (
  objkind TEXT COLLATE NOCASE,
  objname TEXT COLLATE NOCASE,
  subname TEXT COLLATE NOCASE DEFAULT '',
  comment TEXT
)`
