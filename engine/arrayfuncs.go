package engine

import (
	"database/sql/driver"
	"encoding/json"
)

// Arrays are stored as JSON arrays in TEXT cells (see the protocol's array.go).
// These back the Postgres array functions over that representation.

// isJSONArrayText reports whether s parses as a JSON array. Range text like
// "[1,10)" is not valid JSON, so this distinguishes a stored array from a range
// (the ambiguous closed form "[1,5]" is treated as an array).
func isJSONArrayText(s string) bool {
	var arr []json.RawMessage
	return json.Unmarshal([]byte(s), &arr) == nil
}

// jsonArraysOverlap reports whether two stored JSON arrays share an element
// (the Postgres array "&&" operator). The second return is false when either
// value isn't a JSON array.
func jsonArraysOverlap(a, b string) (overlap, ok bool) {
	var ax, bx []json.RawMessage
	if json.Unmarshal([]byte(a), &ax) != nil || json.Unmarshal([]byte(b), &bx) != nil {
		return false, false
	}
	seen := make(map[string]bool, len(ax))
	for _, e := range ax {
		seen[string(e)] = true
	}
	for _, e := range bx {
		if seen[string(e)] {
			return true, true
		}
	}
	return false, true
}

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
