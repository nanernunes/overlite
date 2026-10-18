package engine

import (
	"database/sql/driver"
	"encoding/json"
	"regexp"
	"strings"
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

// reIntVector matches the text form of int2vector/oidvector ("1 2 3"), the type
// of the catalog's pg_index.indkey/indclass/indoption.
var reIntVector = regexp.MustCompile(`^-?\d+(?: -?\d+)*$`)

// arrayAsJSON returns v as a JSON array whatever form the array arrived in: a
// stored JSON array as is, Postgres' '{a,b}' text, or a vector's "1 2". It is
// what unnest and generate_subscripts expand, so they work on the catalog's
// vectors as well as on stored arrays. Anything else is NULL: no elements.
func arrayAsJSON(v driver.Value) driver.Value {
	s := strings.TrimSpace(argStr(v))
	switch {
	case s == "":
		return nil
	case isJSONArrayText(s):
		return s
	case reIntVector.MatchString(s):
		return "[" + strings.Join(strings.Fields(s), ",") + "]"
	case strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
		inner := strings.TrimSpace(s[1 : len(s)-1])
		if inner == "" {
			return "[]"
		}
		parts := strings.Split(inner, ",")
		for i, p := range parts {
			p = strings.Trim(strings.TrimSpace(p), `"`)
			switch {
			case strings.EqualFold(p, "null"):
				parts[i] = "null"
			case reIntVector.MatchString(p):
				parts[i] = p
			default:
				b, _ := json.Marshal(p)
				parts[i] = string(b)
			}
		}
		return "[" + strings.Join(parts, ",") + "]"
	}
	return nil
}

// arrayLower is the subscript of an array's first element: 1, except for the
// catalog's int2vector/oidvector columns, which Postgres numbers from 0
// (generate_subscripts(indkey, 1) starts at 0).
func arrayLower(v driver.Value) driver.Value {
	if reIntVector.MatchString(strings.TrimSpace(argStr(v))) && !isJSONArrayText(argStr(v)) {
		return int64(0)
	}
	return int64(1)
}
