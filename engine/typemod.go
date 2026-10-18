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

// binaryPrecision is the precision Postgres reports, in bits, for the integer
// and floating-point types, whose precision is fixed by the type itself.
func binaryPrecision(decl string) (int64, bool) {
	switch sqliteTypeOID(decl) {
	case typeOIDInt2:
		return 16, true
	case typeOIDInt4:
		return 32, true
	case typeOIDInt8:
		return 64, true
	case typeOIDFloat8:
		return 53, true
	}
	return 0, false
}

// typeNumericPrecision returns the declared precision of a numeric type, the
// fixed binary precision of an integer or float, or NULL (plain unqualified
// numeric has no fixed precision).
func typeNumericPrecision(decl string) driver.Value {
	base, nums := typeParen(decl)
	if numericTypes[base] && len(nums) >= 1 {
		return nums[0]
	}
	if p, ok := binaryPrecision(decl); ok {
		return p
	}
	return nil
}

// typeNumericPrecisionRadix is the base numeric_precision is counted in: 10 for
// a declared numeric, 2 for the binary types, NULL for everything else.
func typeNumericPrecisionRadix(decl string) driver.Value {
	base, nums := typeParen(decl)
	if numericTypes[base] && len(nums) >= 1 {
		return int64(10)
	}
	if _, ok := binaryPrecision(decl); ok {
		return int64(2)
	}
	return nil
}

// typeNumericScale returns the declared scale of a numeric type: the second
// paren argument, defaulting to 0 when only a precision is given (as Postgres
// does), 0 for an integer, or NULL when unqualified.
func typeNumericScale(decl string) driver.Value {
	base, nums := typeParen(decl)
	if !numericTypes[base] || len(nums) == 0 {
		switch sqliteTypeOID(decl) {
		case typeOIDInt2, typeOIDInt4, typeOIDInt8:
			return int64(0)
		}
		return nil
	}
	if len(nums) >= 2 {
		return nums[1]
	}
	return int64(0)
}

// typeDatetimePrecision returns the fractional-second digits of a date/time
// type: the declared precision (TIMESTAMP(3) -> 3), Postgres' default of 6 when
// none is given, 0 for a date, NULL for any other type.
func typeDatetimePrecision(decl string) driver.Value {
	switch sqliteTypeOID(decl) {
	case typeOIDDate:
		return int64(0)
	case typeOIDTimestamp, typeOIDTimestamptz:
		if _, nums := typeParen(decl); len(nums) >= 1 {
			return nums[0]
		}
		return int64(6)
	}
	return nil
}

// typeAttTypmod encodes the declared modifiers the way pg_attribute.atttypmod
// holds them: the precision for a timestamp, the length plus 4 for a character
// type, ((precision << 16) | scale) + 4 for a numeric, and -1 for none.
func typeAttTypmod(decl string) int64 {
	base, nums := typeParen(decl)
	if len(nums) == 0 {
		return -1
	}
	switch {
	case charTypes[base]:
		return nums[0] + 4
	case numericTypes[base]:
		scale := int64(0)
		if len(nums) >= 2 {
			scale = nums[1]
		}
		return (nums[0]<<16 | scale) + 4
	}
	switch sqliteTypeOID(decl) {
	case typeOIDTimestamp, typeOIDTimestamptz:
		return nums[0]
	}
	return -1
}

// formatTypeMod renders a type the way format_type(oid, typmod) does, with the
// modifiers typeAttTypmod encoded: timestamp(3) without time zone,
// character varying(36), numeric(10,2).
func formatTypeMod(oid, typmod int64) string {
	name := formatTypeName(oid)
	if typmod < 0 {
		return name
	}
	switch oid {
	case typeOIDTimestamp:
		return "timestamp(" + strconv.FormatInt(typmod, 10) + ") without time zone"
	case typeOIDTimestamptz:
		return "timestamp(" + strconv.FormatInt(typmod, 10) + ") with time zone"
	case typeOIDVarchar:
		return name + "(" + strconv.FormatInt(typmod-4, 10) + ")"
	case typeOIDNumeric:
		t := typmod - 4
		return "numeric(" + strconv.FormatInt(t>>16, 10) + "," + strconv.FormatInt(t&0xffff, 10) + ")"
	}
	return name
}
