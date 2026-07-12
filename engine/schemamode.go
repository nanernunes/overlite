package engine

import "os"

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

func readSchemaMode() { schemaFilesMode = os.Getenv("OVERLITE_MULTITENANT_SCHEMA") == "true" }

// schemasTableDDL tracks the user schemas in single-file mode (the source of
// truth for pg_namespace and search_path). Public is implicit and never listed.
const schemasTableDDL = `CREATE TABLE IF NOT EXISTS _overlite_schemas (
  name TEXT PRIMARY KEY COLLATE NOCASE
)`
