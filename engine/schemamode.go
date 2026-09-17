package engine

import (
	"log"
	"os"
	"strconv"
)

// Schema storage has two modes:
//
//   - single-file (default): every schema lives inside the one SQLite file, a
//     table `sales.orders` is stored under the literal name "sales.orders" in
//     `main`, and the schema list is tracked in `_overlite_schemas`. CREATE/DROP
//     SCHEMA are ordinary transactional writes (no ATTACH), unqualified names
//     resolve via search_path, and cross-schema foreign keys work (one file).
//
//   - multi-file (OVERLITE_MULTITENANT_SCHEMA=true): every schema is a separate
//     attached SQLite file (`system.sales.db`), giving physical per-tenant
//     isolation. This is the older model; CREATE/DROP SCHEMA can't run inside a
//     transaction there because ATTACH/DETACH can't.
//
// schemaFilesMode reports the multi-file mode. It is set from the environment in
// Open (like catalogDBName) so tests can flip it with t.Setenv before opening.
var schemaFilesMode bool

// readSchemaMode reads OVERLITE_MULTITENANT_SCHEMA.
//
// It accepts anything strconv.ParseBool does, not the single spelling "true":
// setting it to 1, TRUE or yes used to fall back to the single-file default
// without a word, and the difference is invisible until two tenants are found
// sharing a file. An unparseable value is reported rather than ignored.
func readSchemaMode() {
	v, ok := os.LookupEnv("OVERLITE_MULTITENANT_SCHEMA")
	if !ok || v == "" {
		schemaFilesMode = false
		return
	}
	on, err := strconv.ParseBool(v)
	if err != nil {
		log.Printf("overlite: OVERLITE_MULTITENANT_SCHEMA=%q is not a boolean; using single-file schemas", v)
		schemaFilesMode = false
		return
	}
	schemaFilesMode = on
}

// schemasTableDDL tracks the user schemas in single-file mode (the source of
// truth for pg_namespace and search_path). Public is implicit and never listed.
const schemasTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_schemas (
  name TEXT PRIMARY KEY COLLATE NOCASE
)`
