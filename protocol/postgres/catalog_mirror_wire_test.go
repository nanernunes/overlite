package postgres_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The catalogs describe a table the way Postgres does, so a migration tool that
// introspects it finds what it created: constraint names and actions, key
// columns, index operator classes, column types with their modifiers.
func TestCatalogMirrorsSchema(t *testing.T) {
	conn := connect(t, startServer(t))
	// Prisma's DDL: named constraints added after the tables.
	mustExec(t, conn, `CREATE TABLE "School" ("id" TEXT NOT NULL, "createdAt" TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP,
		CONSTRAINT "School_pkey" PRIMARY KEY ("id"))`)
	mustExec(t, conn, `CREATE TABLE "Subject" ("id" TEXT NOT NULL, "schoolId" TEXT NOT NULL, "rank" INTEGER,
		"code" VARCHAR(36), "total" BIGINT, CONSTRAINT "Subject_pkey" PRIMARY KEY ("id"))`)
	mustExec(t, conn, `CREATE INDEX "Subject_schoolId_rank_idx" ON "Subject"("schoolId", "rank" DESC)`)
	mustExec(t, conn, `ALTER TABLE "Subject" ADD CONSTRAINT "Subject_schoolId_fkey" FOREIGN KEY ("schoolId")
		REFERENCES "School"("id") ON DELETE CASCADE ON UPDATE SET NULL`)
	// An unnamed one gets Postgres' default name.
	mustExec(t, conn, `CREATE TABLE note (id int PRIMARY KEY, subject_id text REFERENCES "Subject"(id) ON DELETE RESTRICT)`)

	assert.Equal(t, []string{
		"Subject_schoolId_fkey:c:n:[2]:[1]",
		"note_subject_id_fkey:r:a:[2]:[1]",
	}, queryColumn(t, conn, `SELECT conname || ':' || confdeltype || ':' || confupdtype || ':' || conkey || ':' || confkey
		FROM pg_constraint WHERE contype = 'f' ORDER BY conname`, 0))
	assert.Equal(t, []string{"School_pkey", "Subject_pkey", "note_pkey"}, queryColumn(t, conn,
		`SELECT conname FROM pg_constraint WHERE contype = 'p' ORDER BY conname`, 0))

	// Every index column has the type's default btree operator class, and a
	// DESC column says so in indoption.
	assert.Equal(t, []string{"2 3:3126 1978:0 3:btree"}, queryColumn(t, conn,
		`SELECT i.indkey || ':' || i.indclass || ':' || i.indoption || ':' || am.amname
		 FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_am am ON am.oid = c.relam
		 WHERE c.relname = 'Subject_schoolId_rank_idx'`, 0))
	assert.Equal(t, []string{"int4_ops:t", "text_ops:t"}, queryColumn(t, conn,
		`SELECT opcname || ':' || CASE WHEN opcdefault THEN 't' ELSE 'f' END FROM pg_opclass
		 WHERE oid IN (3126, 1978) ORDER BY opcname`, 0))

	// Tables have a row type; indexes don't.
	assert.Equal(t, []string{"School:true", "School_pkey:false"}, queryColumn(t, conn,
		`SELECT relname || ':' || CASE WHEN reltype > 0 THEN 'true' ELSE 'false' END FROM pg_class
		 WHERE relname IN ('School', 'School_pkey') ORDER BY relname`, 0))

	// Column types keep their modifiers and widths.
	assert.Equal(t, []string{
		"code:character varying(36):character varying:varchar:::",
		"createdAt:timestamp(3) without time zone:timestamp without time zone:timestamp:::3",
		"id:text:text:text:::",
		"rank:integer:integer:int4:32:0:",
		"schoolId:text:text:text:::",
		"total:bigint:bigint:int8:64:0:",
	}, queryColumn(t, conn, `SELECT info.column_name || ':' || format_type(att.atttypid, att.atttypmod) || ':' ||
		info.data_type || ':' || info.udt_name || ':' || coalesce(info.numeric_precision, '') || ':' ||
		coalesce(info.numeric_scale, '') || ':' || coalesce(info.datetime_precision, '')
		FROM information_schema.columns info
		JOIN pg_attribute att ON att.attname = info.column_name
		JOIN pg_class c ON c.oid = att.attrelid AND c.relname = info.table_name
		WHERE info.table_name = 'Subject' OR info.column_name = 'createdAt'
		ORDER BY info.column_name`, 0))
}
