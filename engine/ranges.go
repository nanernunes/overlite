package engine

import (
	"database/sql/driver"
	"fmt"
	"math/big"
	"strings"
)

// argStr renders a constructor argument, mapping NULL to an empty (infinite) bound.
func argStr(v driver.Value) string {
	if v == nil {
		return ""
	}
	return fmt.Sprint(v)
}

// Range types over SQLite: stored as the Postgres range text (`[1,10)`) in a
// TEXT cell (plain-SQLite readable). Constructors build the text, accessors read
// the bounds, and `@>` containment is folded into json_contains.

// makeRange builds a range literal from lower, upper, and bounds ("[)", "[]", …).
func makeRange(lo, hi, bounds string) string {
	lb, ub := "[", ")"
	if len(bounds) == 2 {
		lb, ub = string(bounds[0]), string(bounds[1])
	}
	// [x,x) (and other non-fully-closed equal bounds) is the empty range.
	if lo == hi && !(lb == "[" && ub == "]") {
		return "empty"
	}
	if a, b, ok := ratPair(lo, hi); ok && a.Cmp(b) > 0 {
		return "empty"
	}
	return lb + lo + "," + hi + ub
}

// isRangeText reports whether s is a range literal.
func isRangeText(s string) bool {
	if s == "empty" {
		return true
	}
	if len(s) < 3 || (s[0] != '[' && s[0] != '(') {
		return false
	}
	last := s[len(s)-1]
	return (last == ')' || last == ']') && strings.Contains(s, ",")
}

type rangeVal struct {
	empty        bool
	lo, hi       string
	loInc, hiInc bool
	loInf, hiInf bool
}

func parseRange(s string) (rangeVal, bool) {
	if s == "empty" {
		return rangeVal{empty: true}, true
	}
	if !isRangeText(s) {
		return rangeVal{}, false
	}
	r := rangeVal{loInc: s[0] == '[', hiInc: s[len(s)-1] == ']'}
	inner := s[1 : len(s)-1]
	comma := strings.IndexByte(inner, ',')
	if comma < 0 {
		return rangeVal{}, false
	}
	r.lo = strings.TrimSpace(inner[:comma])
	r.hi = strings.TrimSpace(inner[comma+1:])
	r.loInf = r.lo == ""
	r.hiInf = r.hi == ""
	return r, true
}

// rangeContains reports whether range a contains the scalar value x.
func rangeContains(a, x string) bool {
	r, ok := parseRange(a)
	if !ok || r.empty {
		return false
	}
	xv, ok := ratFromString(x)
	if !ok {
		return false
	}
	if !r.loInf {
		lv, _ := ratFromString(r.lo)
		c := xv.Cmp(lv)
		if c < 0 || (c == 0 && !r.loInc) {
			return false
		}
	}
	if !r.hiInf {
		hv, _ := ratFromString(r.hi)
		c := xv.Cmp(hv)
		if c > 0 || (c == 0 && !r.hiInc) {
			return false
		}
	}
	return true
}

func ratPair(a, b string) (*big.Rat, *big.Rat, bool) {
	x, okx := ratFromString(a)
	y, oky := ratFromString(b)
	return x, y, okx && oky
}
