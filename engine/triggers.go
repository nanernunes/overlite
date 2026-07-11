package engine

import (
	"strconv"
	"strings"
	"sync"
)

// triggerDefByOID backs the process-global pg_get_triggerdef() scalar (which has
// no DB access) with each trigger's CREATE statement. Refreshed from the main
// database's sqlite_master on every connection setup, so a trigger created in a
// prior session (or by another connection) is rendered by psql's \d on connect.
// Keys are the pg_trigger oids (rowid + 70000000 for the public schema).
var triggerDefByOID sync.Map

// refreshTriggerDefs stores oid\x1fsql pairs (produced by the query in
// setupConnection) into the registry.
func refreshTriggerDefs(pairs []string) {
	for _, p := range pairs {
		i := strings.IndexByte(p, '\x1f')
		if i < 0 {
			continue
		}
		oid, err := strconv.ParseInt(p[:i], 10, 64)
		if err != nil {
			continue
		}
		triggerDefByOID.Store(oid, p[i+1:])
	}
}

func lookupTriggerDef(oid int64) string {
	if v, ok := triggerDefByOID.Load(oid); ok {
		return v.(string)
	}
	return ""
}
