package engine

import (
	"encoding/json"
	"sort"
	"strings"
)

// hstore over SQLite: a key/value map stored as a JSON object in a TEXT cell, so
// the existing json1 machinery (-> access, ? existence) works on it and the file
// stays plain-SQLite readable. Input parses the `k=>v` text form to JSON; output
// renders JSON back to hstore text.

// hstoreIn parses an hstore literal ("a=>1, b=>2") into a JSON object.
func hstoreIn(s string) string {
	m := map[string]any{}
	i := 0
	for i < len(s) {
		i = skipWS(s, i)
		if i >= len(s) {
			break
		}
		key, ni, ok := hstoreToken(s, i)
		if !ok {
			break
		}
		i = skipWS(s, ni)
		if i+1 >= len(s) || s[i] != '=' || s[i+1] != '>' {
			break
		}
		i = skipWS(s, i+2)
		val, ni2, isNull := hstoreValue(s, i)
		i = ni2
		if isNull {
			m[key] = nil
		} else {
			m[key] = val
		}
		i = skipWS(s, i)
		if i < len(s) && s[i] == ',' {
			i++
		}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// hstoreToken reads a quoted or bare key token.
func hstoreToken(s string, i int) (string, int, bool) {
	if i >= len(s) {
		return "", i, false
	}
	if s[i] == '"' {
		return hstoreQuoted(s, i)
	}
	start := i
	for i < len(s) && s[i] != ' ' && s[i] != '\t' && s[i] != '=' && s[i] != ',' {
		i++
	}
	if i == start {
		return "", i, false
	}
	return s[start:i], i, true
}

// hstoreValue reads a value, reporting the unquoted NULL keyword.
func hstoreValue(s string, i int) (val string, next int, isNull bool) {
	if i < len(s) && s[i] == '"' {
		v, ni, _ := hstoreQuoted(s, i)
		return v, ni, false
	}
	start := i
	for i < len(s) && s[i] != ',' && s[i] != ' ' && s[i] != '\t' {
		i++
	}
	tok := s[start:i]
	if strings.EqualFold(tok, "null") {
		return "", i, true
	}
	return tok, i, false
}

func hstoreQuoted(s string, i int) (string, int, bool) {
	i++ // opening quote
	var b strings.Builder
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			b.WriteByte(s[i+1])
			i += 2
			continue
		}
		if c == '"' {
			return b.String(), i + 1, true
		}
		b.WriteByte(c)
		i++
	}
	return b.String(), i, true
}

func skipWS(s string, i int) int {
	for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
		i++
	}
	return i
}

// hstoreOut renders a JSON object as hstore text, keys sorted for a stable form.
func hstoreOut(jsonText string) string {
	m := hstoreMap(jsonText)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = hstoreQuote(k) + "=>" + hstoreQuoteVal(m[k])
	}
	return strings.Join(parts, ", ")
}

func hstoreMap(jsonText string) map[string]any {
	var m map[string]any
	if json.Unmarshal([]byte(jsonText), &m) != nil {
		return nil
	}
	return m
}

func hstoreQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

func hstoreQuoteVal(v any) string {
	if v == nil {
		return "NULL"
	}
	return hstoreQuote(valToString(v))
}

func valToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		return trimNum(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func trimNum(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// hstoreKeys / hstoreVals return the keys / values as a JSON array (the array
// storage format), sorted by key.
func hstoreKeys(jsonText string) string {
	m := hstoreMap(jsonText)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b, _ := json.Marshal(keys)
	return string(b)
}

func hstoreVals(jsonText string) string {
	m := hstoreMap(jsonText)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	vals := make([]string, len(keys))
	for i, k := range keys {
		vals[i] = valToString(m[k])
	}
	b, _ := json.Marshal(vals)
	return string(b)
}
