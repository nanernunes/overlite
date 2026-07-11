package engine

import (
	"database/sql/driver"
	"encoding/json"
)

// Arrays are stored as JSON arrays in TEXT cells (see the protocol's array.go).
// These back the Postgres array functions over that representation.

// jsonArrayLen parses a stored JSON array value and returns its length.
func jsonArrayLen(v driver.Value) (int64, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case []byte:
		s = string(x)
	default:
		return 0, false
	}
	var arr []json.RawMessage
	if json.Unmarshal([]byte(s), &arr) != nil {
		return 0, false
	}
	return int64(len(arr)), true
}
