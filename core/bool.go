package core

import "strings"

// ParseBool reads a boolean the way Postgres' boolin does: surrounding space
// is ignored, case does not matter, and any unique prefix of true, false,
// yes or no counts, as do on, off, 1 and 0. ok is false for anything else.
func ParseBool(s string) (value, ok bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return false, false
	}
	switch s {
	case "1", "on":
		return true, true
	case "0", "of", "off":
		return false, true
	}
	for _, w := range []struct {
		word  string
		value bool
	}{{"true", true}, {"yes", true}, {"false", false}, {"no", false}} {
		if strings.HasPrefix(w.word, s) {
			return w.value, true
		}
	}
	return false, false
}
