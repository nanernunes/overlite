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
		lv, ok := ratFromString(r.lo)
		if !ok {
			return false
		}
		c := xv.Cmp(lv)
		if c < 0 || (c == 0 && !r.loInc) {
			return false
		}
	}
	if !r.hiInf {
		hv, ok := ratFromString(r.hi)
		if !ok {
			return false
		}
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

// rangeContainsRange reports whether range a contains range b (every point of b
// is in a). The empty range is contained by every range and contains only
// itself.
func rangeContainsRange(a, b string) bool {
	ra, oka := parseRange(a)
	rb, okb := parseRange(b)
	if !oka || !okb {
		return false
	}
	if rb.empty {
		return true
	}
	if ra.empty {
		return false
	}
	return lowerCovers(ra, rb) && upperCovers(ra, rb)
}

// lowerCovers reports whether a's lower bound starts no later than b's.
func lowerCovers(a, b rangeVal) bool {
	if a.loInf {
		return true
	}
	if b.loInf {
		return false
	}
	av, bv, ok := ratPair(a.lo, b.lo)
	if !ok {
		return false
	}
	switch av.Cmp(bv) {
	case -1:
		return true
	case 0:
		return a.loInc || !b.loInc
	}
	return false
}

// upperCovers reports whether a's upper bound ends no earlier than b's.
func upperCovers(a, b rangeVal) bool {
	if a.hiInf {
		return true
	}
	if b.hiInf {
		return false
	}
	av, bv, ok := ratPair(a.hi, b.hi)
	if !ok {
		return false
	}
	switch av.Cmp(bv) {
	case 1:
		return true
	case 0:
		return a.hiInc || !b.hiInc
	}
	return false
}

// rangeOverlaps reports whether two ranges share at least one point.
func rangeOverlaps(a, b string) bool {
	ra, oka := parseRange(a)
	rb, okb := parseRange(b)
	if !oka || !okb || ra.empty || rb.empty {
		return false
	}
	return startsBeforeEnd(ra, rb) && startsBeforeEnd(rb, ra)
}

// startsBeforeEnd reports whether x's lower bound is at or before y's upper
// bound — one of the two conditions for overlap.
func startsBeforeEnd(x, y rangeVal) bool {
	if x.loInf || y.hiInf {
		return true
	}
	lo, hi, ok := ratPair(x.lo, y.hi)
	if !ok {
		return false
	}
	switch lo.Cmp(hi) {
	case -1:
		return true
	case 0:
		return x.loInc && y.hiInc
	}
	return false
}
