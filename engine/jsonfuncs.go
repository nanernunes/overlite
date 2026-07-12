package engine

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// jsonbExists backs the jsonb `?` operator: does key exist as a top-level object
// key, or (for an array) as a string element?
func jsonbExists(j, key string) bool {
	var v any
	if json.Unmarshal([]byte(j), &v) != nil {
		return false
	}
	switch t := v.(type) {
	case map[string]any:
		_, ok := t[key]
		return ok
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok && s == key {
				return true
			}
		}
	}
	return false
}

// jsonbExistsAny / jsonbExistsAll back `?|` / `?&`; keys arrives as a JSON array.
func jsonbExistsAny(j, keysJSON string) bool {
	for _, k := range jsonStrArray(keysJSON) {
		if jsonbExists(j, k) {
			return true
		}
	}
	return false
}

func jsonbExistsAll(j, keysJSON string) bool {
	keys := jsonStrArray(keysJSON)
	for _, k := range keys {
		if !jsonbExists(j, k) {
			return false
		}
	}
	return true
}

func jsonStrArray(s string) []string {
	var arr []any
	if json.Unmarshal([]byte(s), &arr) != nil {
		return nil
	}
	out := make([]string, len(arr))
	for i, e := range arr {
		out[i] = fmt.Sprint(e)
	}
	return out
}

// jsonbContains implements Postgres' jsonb @> jsonb (does a contain b?). Both
// arguments are JSON text; it backs the json_contains() scalar the dialect layer
// emits for the @> / <@ operators. Invalid JSON is treated as non-containing.
func jsonbContains(a, b string) bool {
	var av, bv any
	if json.Unmarshal([]byte(a), &av) != nil {
		return false
	}
	if json.Unmarshal([]byte(b), &bv) != nil {
		return false
	}
	return jsonContains(av, bv)
}

// jsonContains reports whether a contains b, matching Postgres jsonb semantics:
// objects match on keys (recursively), arrays require every b-element to be
// contained in some a-element, and a scalar is contained in an array holding it.
func jsonContains(a, b any) bool {
	switch bv := b.(type) {
	case map[string]any:
		am, ok := a.(map[string]any)
		if !ok {
			return false
		}
		for k, v := range bv {
			av, ok := am[k]
			if !ok || !jsonContains(av, v) {
				return false
			}
		}
		return true
	case []any:
		aa, ok := a.([]any)
		if !ok {
			return false
		}
		for _, be := range bv {
			if !anyContains(aa, be) {
				return false
			}
		}
		return true
	default: // scalar
		if aa, ok := a.([]any); ok {
			return anyContains(aa, bv)
		}
		return reflect.DeepEqual(a, b)
	}
}

func anyContains(arr []any, b any) bool {
	for _, e := range arr {
		if jsonContains(e, b) {
			return true
		}
	}
	return false
}
