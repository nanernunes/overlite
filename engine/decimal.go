package engine

import (
	"database/sql/driver"
	"math/big"
	"strings"

	sqlite "modernc.org/sqlite"
)

// decSum is an exact SUM aggregate over decimal text (dec_sum).
type decSum struct {
	acc *big.Rat
	any bool
}

func (a *decSum) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) == 0 || args[0] == nil {
		return nil
	}
	if r, ok := parseDec(args[0]); ok {
		if a.acc == nil {
			a.acc = new(big.Rat)
		}
		a.acc.Add(a.acc, r)
		a.any = true
	}
	return nil
}

func (a *decSum) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) > 0 && args[0] != nil {
		if r, ok := parseDec(args[0]); ok && a.acc != nil {
			a.acc.Sub(a.acc, r)
		}
	}
	return nil
}

func (a *decSum) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) { return a.final(), nil }
func (a *decSum) Final(_ *sqlite.FunctionContext)                             {}

func (a *decSum) final() driver.Value {
	if !a.any || a.acc == nil {
		return nil
	}
	return decString(a.acc)
}

// decAgg overrides the built-in sum/avg so they are exact over decimal-text
// (numeric) columns, while preserving the native return type for int/real
// columns (int64 / float64) so behavior elsewhere is unchanged.
type decAgg struct {
	acc              *big.Rat
	n                int64
	avg              bool
	sawStr, sawFloat bool
}

func (a *decAgg) Step(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) == 0 || args[0] == nil {
		return nil
	}
	switch args[0].(type) {
	case string, []byte:
		a.sawStr = true
	case float64:
		a.sawFloat = true
	}
	if r, ok := parseDec(args[0]); ok {
		if a.acc == nil {
			a.acc = new(big.Rat)
		}
		a.acc.Add(a.acc, r)
		a.n++
	}
	return nil
}

func (a *decAgg) WindowInverse(_ *sqlite.FunctionContext, args []driver.Value) error {
	if len(args) > 0 && args[0] != nil {
		if r, ok := parseDec(args[0]); ok && a.acc != nil {
			a.acc.Sub(a.acc, r)
			a.n--
		}
	}
	return nil
}

func (a *decAgg) WindowValue(_ *sqlite.FunctionContext) (driver.Value, error) { return a.value(), nil }
func (a *decAgg) Final(_ *sqlite.FunctionContext)                             {}

func (a *decAgg) value() driver.Value {
	if a.n == 0 || a.acc == nil {
		return nil // empty / all-NULL -> NULL (matches sum/avg)
	}
	res := new(big.Rat).Set(a.acc)
	if a.avg {
		res.Quo(res, new(big.Rat).SetInt64(a.n))
	}
	switch {
	case a.sawStr: // a numeric column -> exact decimal
		return decString(res)
	case a.avg, a.sawFloat: // real column -> float, as SQLite does
		f, _ := res.Float64()
		return f
	case res.IsInt() && res.Num().IsInt64(): // sum of ints -> int
		return res.Num().Int64()
	default:
		return decString(res)
	}
}

// Exact numeric over SQLite. A `numeric`/`decimal` column is stored as its exact
// decimal string in a TEXT cell (plain-SQLite readable). SQLite's operators are
// float, so overlite routes numeric arithmetic/aggregation through these
// math/big.Rat-backed functions, and compares/orders via the DECIMAL collation.

// parseDec parses a decimal value (text or numeric) into a big.Rat.
func parseDec(v driver.Value) (*big.Rat, bool) {
	switch x := v.(type) {
	case nil:
		return nil, false
	case int64:
		return new(big.Rat).SetInt64(x), true
	case float64:
		r := new(big.Rat)
		r.SetFloat64(x)
		return r, true
	case string:
		return ratFromString(x)
	case []byte:
		return ratFromString(string(x))
	}
	return nil, false
}

func ratFromString(s string) (*big.Rat, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}
	r, ok := new(big.Rat).SetString(s)
	return r, ok
}

// decString renders a big.Rat as a plain decimal string, without an exponent and
// trimming to a reasonable scale for non-terminating results (e.g. division).
func decString(r *big.Rat) string {
	if r.IsInt() {
		return r.Num().String()
	}
	// A terminating decimal prints exactly; otherwise cap at 20 fractional digits.
	prec := decimalDigits(r)
	if prec < 0 || prec > 20 {
		prec = 20
	}
	s := r.FloatString(prec)
	if strings.Contains(s, ".") { // trim trailing zeros a terminating value gained
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return s
}

// decimalDigits returns the number of fractional digits needed to represent r
// exactly, or -1 if it does not terminate in base 10 (denominator has a prime
// factor other than 2 or 5).
func decimalDigits(r *big.Rat) int {
	den := new(big.Int).Set(r.Denom())
	c2, c5 := factorCount(den, 2), factorCount(den, 5)
	rest := new(big.Int).Set(den)
	for _, p := range []int64{2, 5} {
		bp := big.NewInt(p)
		for new(big.Int).Mod(rest, bp).Sign() == 0 {
			rest.Div(rest, bp)
		}
	}
	if rest.Cmp(big.NewInt(1)) != 0 {
		return -1
	}
	if c2 > c5 {
		return c2
	}
	return c5
}

func factorCount(n *big.Int, p int64) int {
	bp := big.NewInt(p)
	m := new(big.Int).Set(n)
	c := 0
	for new(big.Int).Mod(m, bp).Sign() == 0 {
		m.Div(m, bp)
		c++
	}
	return c
}

// binDec applies a binary decimal operation to two values, returning the exact
// decimal string (or nil if either operand isn't numeric).
func binDec(a, b driver.Value, op func(z, x, y *big.Rat) *big.Rat) (driver.Value, error) {
	x, okx := parseDec(a)
	y, oky := parseDec(b)
	if !okx || !oky {
		return nil, nil
	}
	return decString(op(new(big.Rat), x, y)), nil
}

// decCmp returns -1/0/1 comparing two decimal values (used by the collation and
// by dec_cmp).
func decCmp(a, b driver.Value) int {
	x, okx := parseDec(a)
	y, oky := parseDec(b)
	switch {
	case !okx && !oky:
		return strings.Compare(toStr(a), toStr(b))
	case !okx:
		return -1
	case !oky:
		return 1
	default:
		return x.Cmp(y)
	}
}

func toStr(v driver.Value) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return ""
}

// decRound rounds r to scale fractional digits (half-up).
func decRound(r *big.Rat, scale int) *big.Rat {
	shift := new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	scaled := new(big.Rat).Mul(r, shift)
	// round half up to nearest integer
	f := new(big.Float).SetPrec(200).SetRat(scaled)
	half := big.NewFloat(0.5)
	if f.Sign() < 0 {
		half.Neg(half)
	}
	f.Add(f, half)
	i, _ := f.Int(nil)
	out := new(big.Rat).SetInt(i)
	return out.Quo(out, shift)
}
