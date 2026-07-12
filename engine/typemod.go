package engine

import (
	"database/sql/driver"
	"strconv"
	"strings"
)

// Type modifiers reported by information_schema.columns are recovered from the
// declared SQLite type: varchar(50) -> character_maximum_length 50, and the
// exact-numeric marker DECIMALTEXT(10,2) -> numeric_precision 10 / scale 2 (the
// (p,s) is kept on the marker precisely so it can be reported here).

// typeParen splits a declared type into its base name (lowercased) and the
// integer arguments inside its parentheses: "DECIMALTEXT(10,2)" -> "decimaltext",
// [10,2].
func typeParen(decl string) (base string, nums []int64) {
	decl = strings.TrimSpace(decl)
	open := strings.IndexByte(decl, '(')
	if open < 0 {
		return strings.ToLower(decl), nil
	}
	base = strings.ToLower(strings.TrimSpace(decl[:open]))
	close := strings.IndexByte(decl, ')')
	if close < open {
		return base, nil
	}
	for _, p := range strings.Split(decl[open+1:close], ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64); err == nil {
			nums = append(nums, n)
		}
	}
	return base, nums
}

var charTypes = map[string]bool{
	"varchar": true, "character varying": true,
	"char": true, "character": true, "bpchar": true,
}

var numericTypes = map[string]bool{
	"decimaltext": true, "numeric": true, "decimal": true,
}

// typeCharMaxLength returns the declared length of a character type, or NULL.
func typeCharMaxLength(decl string) driver.Value {
	base, nums := typeParen(decl)
	if charTypes[base] && len(nums) >= 1 {
		return nums[0]
	}
	return nil
}

// typeNumericPrecision returns the declared precision of a numeric type, or NULL
// (plain unqualified numeric has no fixed precision).
func typeNumericPrecision(decl string) driver.Value {
	base, nums := typeParen(decl)
	if numericTypes[base] && len(nums) >= 1 {
		return nums[0]
	}
	return nil
}

// typeNumericScale returns the declared scale of a numeric type: the second
// paren argument, defaulting to 0 when only a precision is given (as Postgres
// does), or NULL when unqualified.
func typeNumericScale(decl string) driver.Value {
	base, nums := typeParen(decl)
	if !numericTypes[base] || len(nums) == 0 {
		return nil
	}
	if len(nums) >= 2 {
		return nums[1]
	}
	return int64(0)
}
