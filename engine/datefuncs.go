package engine

import (
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file backs the Postgres date/time functions that don't map cleanly onto
// a SQLite built-in: date_trunc, date_part, and to_char. now()/extract are
// handled by the dialect layer (rewritten to datetime('now') / date_part).

var timeLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05Z07:00",
	"2006-01-02T15:04:05",
	"2006-01-02",
	"15:04:05",
}

func parseTime(v driver.Value) (time.Time, bool) {
	if v == nil {
		return time.Time{}, false
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	for _, l := range timeLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// dateTrunc implements Postgres date_trunc(unit, ts): zeroes out everything
// below `unit`.
func dateTrunc(unit string, t time.Time) string {
	const ts = "2006-01-02 15:04:05"
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "year":
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location()).Format(ts)
	case "quarter":
		m := (int(t.Month())-1)/3*3 + 1
		return time.Date(t.Year(), time.Month(m), 1, 0, 0, 0, 0, t.Location()).Format(ts)
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location()).Format(ts)
	case "week":
		// ISO week starts on Monday.
		wd := (int(t.Weekday()) + 6) % 7
		d := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
		return d.AddDate(0, 0, -wd).Format(ts)
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location()).Format(ts)
	case "hour":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location()).Format(ts)
	case "minute":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location()).Format(ts)
	default: // second (and unknown)
		return t.Truncate(time.Second).Format(ts)
	}
}

// datePart implements Postgres date_part(field, ts) / extract(field FROM ts).
func datePart(field string, t time.Time) float64 {
	switch strings.ToLower(strings.TrimSpace(field)) {
	case "century":
		return float64((t.Year() + 99) / 100)
	case "year", "isoyear":
		return float64(t.Year())
	case "quarter":
		return float64((int(t.Month())-1)/3 + 1)
	case "month":
		return float64(t.Month())
	case "week":
		_, w := t.ISOWeek()
		return float64(w)
	case "day":
		return float64(t.Day())
	case "hour":
		return float64(t.Hour())
	case "minute":
		return float64(t.Minute())
	case "second":
		return float64(t.Second())
	case "dow": // 0=Sunday
		return float64(t.Weekday())
	case "isodow": // 1=Monday..7=Sunday
		if t.Weekday() == time.Sunday {
			return 7
		}
		return float64(t.Weekday())
	case "doy":
		return float64(t.YearDay())
	case "epoch":
		return float64(t.Unix())
	default:
		return 0
	}
}

// toChar implements Postgres to_char(ts, fmt) via a token scanner over the
// format string: it matches the longest known token at each position, honors an
// "FM" prefix (suppress zero-padding / name-padding), and passes "double-quoted"
// text through literally. Computed fields (day-of-year, quarter, ISO week,
// Julian day, ...) are produced directly rather than mapped onto a Go layout.
func toChar(t time.Time, format string) string {
	var b strings.Builder
	for i := 0; i < len(format); {
		if format[i] == '"' { // quoted literal text
			j := i + 1
			for j < len(format) && format[j] != '"' {
				b.WriteByte(format[j])
				j++
			}
			i = j + 1
			continue
		}
		fm := false
		rest := format[i:]
		if len(rest) >= 2 && (rest[0] == 'F' || rest[0] == 'f') && (rest[1] == 'M' || rest[1] == 'm') {
			fm, rest = true, rest[2:]
		}
		if val, n := matchToCharToken(t, rest, fm); n > 0 {
			b.WriteString(val)
			i += n
			if fm {
				i += 2
			}
			continue
		}
		if fm { // "FM" not followed by a token: drop the modifier
			i += 2
			continue
		}
		b.WriteByte(format[i])
		i++
	}
	return b.String()
}

// tcToken is one to_char format token and its producer.
type tcToken struct {
	tok string
	f   func(t time.Time, fm bool) string
}

// tcTokens is ordered longest-first so e.g. "HH24" wins over "HH". Numeric
// tokens are conventionally upper-case; the name tokens carry their own case.
var tcTokens = []tcToken{
	{"IYYY", func(t time.Time, fm bool) string { y, _ := t.ISOWeek(); return tcNum(fm, 4, y) }},
	{"YYYY", func(t time.Time, fm bool) string { return tcNum(fm, 4, t.Year()) }},
	{"HH24", func(t time.Time, fm bool) string { return tcNum(fm, 2, t.Hour()) }},
	{"HH12", func(t time.Time, fm bool) string { return tcNum(fm, 2, tc12(t)) }},
	{"MONTH", func(t time.Time, fm bool) string { return tcName(fm, strings.ToUpper(t.Month().String())) }},
	{"Month", func(t time.Time, fm bool) string { return tcName(fm, t.Month().String()) }},
	{"month", func(t time.Time, fm bool) string { return tcName(fm, strings.ToLower(t.Month().String())) }},
	{"DDD", func(t time.Time, fm bool) string { return tcNum(fm, 3, t.YearDay()) }},
	{"DAY", func(t time.Time, fm bool) string { return tcName(fm, strings.ToUpper(t.Weekday().String())) }},
	{"Day", func(t time.Time, fm bool) string { return tcName(fm, t.Weekday().String()) }},
	{"day", func(t time.Time, fm bool) string { return tcName(fm, strings.ToLower(t.Weekday().String())) }},
	{"MON", func(t time.Time, fm bool) string { return strings.ToUpper(t.Month().String()[:3]) }},
	{"Mon", func(t time.Time, fm bool) string { return t.Month().String()[:3] }},
	{"mon", func(t time.Time, fm bool) string { return strings.ToLower(t.Month().String()[:3]) }},
	{"YYY", func(t time.Time, fm bool) string { return tcLast(t.Year(), 3) }},
	{"DY", func(t time.Time, fm bool) string { return strings.ToUpper(t.Weekday().String()[:3]) }},
	{"Dy", func(t time.Time, fm bool) string { return t.Weekday().String()[:3] }},
	{"dy", func(t time.Time, fm bool) string { return strings.ToLower(t.Weekday().String()[:3]) }},
	{"YY", func(t time.Time, fm bool) string { return tcLast(t.Year(), 2) }},
	{"MM", func(t time.Time, fm bool) string { return tcNum(fm, 2, int(t.Month())) }},
	{"MI", func(t time.Time, fm bool) string { return tcNum(fm, 2, t.Minute()) }},
	{"SS", func(t time.Time, fm bool) string { return tcNum(fm, 2, t.Second()) }},
	{"MS", func(t time.Time, fm bool) string { return tcNum(fm, 3, t.Nanosecond()/1e6) }},
	{"US", func(t time.Time, fm bool) string { return tcNum(fm, 6, t.Nanosecond()/1e3) }},
	{"DD", func(t time.Time, fm bool) string { return tcNum(fm, 2, t.Day()) }},
	{"HH", func(t time.Time, fm bool) string { return tcNum(fm, 2, tc12(t)) }},
	{"WW", func(t time.Time, fm bool) string { return tcNum(fm, 2, (t.YearDay()-1)/7+1) }},
	{"IW", func(t time.Time, fm bool) string { _, w := t.ISOWeek(); return tcNum(fm, 2, w) }},
	{"CC", func(t time.Time, fm bool) string { return tcNum(fm, 2, (t.Year()+99)/100) }},
	{"ID", func(t time.Time, fm bool) string { return strconv.Itoa(tcISODow(t)) }},
	{"AM", func(t time.Time, fm bool) string { return tcMeridiem(t, true) }},
	{"PM", func(t time.Time, fm bool) string { return tcMeridiem(t, true) }},
	{"am", func(t time.Time, fm bool) string { return tcMeridiem(t, false) }},
	{"pm", func(t time.Time, fm bool) string { return tcMeridiem(t, false) }},
	{"TZ", func(t time.Time, fm bool) string { return t.Format("MST") }},
	{"Q", func(t time.Time, fm bool) string { return strconv.Itoa((int(t.Month())-1)/3 + 1) }},
	{"J", func(t time.Time, fm bool) string { return strconv.Itoa(julianDay(t)) }},
	{"D", func(t time.Time, fm bool) string { return strconv.Itoa(int(t.Weekday()) + 1) }},
	{"Y", func(t time.Time, fm bool) string { return tcLast(t.Year(), 1) }},
}

// tcNameCase are the tokens whose letter case is significant (month/day names
// and meridiem). Numeric tokens match case-insensitively (YYYY == yyyy).
var tcNameCase = map[string]bool{
	"MONTH": true, "Month": true, "month": true,
	"DAY": true, "Day": true, "day": true,
	"MON": true, "Mon": true, "mon": true,
	"DY": true, "Dy": true, "dy": true,
	"AM": true, "PM": true, "am": true, "pm": true,
}

func matchToCharToken(t time.Time, s string, fm bool) (string, int) {
	for _, e := range tcTokens {
		n := len(e.tok)
		if len(s) < n {
			continue
		}
		seg := s[:n]
		if tcNameCase[e.tok] {
			if seg == e.tok {
				return e.f(t, fm), n
			}
			continue
		}
		if strings.EqualFold(seg, e.tok) {
			return e.f(t, fm), n
		}
	}
	return "", 0
}

// tcNum zero-pads v to width, unless FM is set.
func tcNum(fm bool, width, v int) string {
	if fm {
		return strconv.Itoa(v)
	}
	return fmt.Sprintf("%0*d", width, v)
}

// tcLast returns the last n digits of a (zero-padded) year.
func tcLast(year, n int) string {
	s := fmt.Sprintf("%04d", year)
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// tcName pads month/day names to 9 chars (Postgres default), unless FM.
func tcName(fm bool, name string) string {
	if fm || len(name) >= 9 {
		return name
	}
	return name + strings.Repeat(" ", 9-len(name))
}

func tc12(t time.Time) int {
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	return h
}

func tcMeridiem(t time.Time, upper bool) string {
	s := "am"
	if t.Hour() >= 12 {
		s = "pm"
	}
	if upper {
		return strings.ToUpper(s)
	}
	return s
}

// tcISODow returns the ISO day of week (Monday=1 .. Sunday=7).
func tcISODow(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// julianDay returns the Julian Day Number for t's date.
func julianDay(t time.Time) int {
	y, m, d := t.Year(), int(t.Month()), t.Day()
	a := (14 - m) / 12
	yy := y + 4800 - a
	mm := m + 12*a - 3
	return d + (153*mm+2)/5 + 365*yy + yy/4 - yy/100 + yy/400 - 32045
}

// scalar function adapters (driver.Value in/out).

func dateTruncFn(args []driver.Value) (driver.Value, error) {
	if len(args) != 2 || args[0] == nil || args[1] == nil {
		return nil, nil
	}
	t, ok := parseTime(args[1])
	if !ok {
		return nil, nil
	}
	return dateTrunc(fmt.Sprint(args[0]), t), nil
}

func datePartFn(args []driver.Value) (driver.Value, error) {
	if len(args) != 2 || args[0] == nil || args[1] == nil {
		return nil, nil
	}
	t, ok := parseTime(args[1])
	if !ok {
		return nil, nil
	}
	return datePart(fmt.Sprint(args[0]), t), nil
}

func toCharFn(args []driver.Value) (driver.Value, error) {
	if len(args) != 2 || args[0] == nil || args[1] == nil {
		return nil, nil
	}
	t, ok := parseTime(args[0])
	if !ok {
		return fmt.Sprint(args[0]), nil
	}
	return toChar(t, fmt.Sprint(args[1])), nil
}

// ageFn implements age(ts) = now() - ts and age(a, b) = a - b, rendered as a
// Postgres calendar interval ("1 year 2 mons 5 days").
func ageFn(args []driver.Value) (driver.Value, error) {
	switch len(args) {
	case 1:
		if args[0] == nil {
			return nil, nil
		}
		b, ok := parseTime(args[0])
		if !ok {
			return nil, nil
		}
		return ageInterval(time.Now(), b), nil
	case 2:
		if args[0] == nil || args[1] == nil {
			return nil, nil
		}
		a, ok1 := parseTime(args[0])
		b, ok2 := parseTime(args[1])
		if !ok1 || !ok2 {
			return nil, nil
		}
		return ageInterval(a, b), nil
	}
	return nil, nil
}

// ageInterval returns the calendar interval a - b as Postgres formats it,
// breaking the difference into years, months, days, and a time component.
func ageInterval(a, b time.Time) string {
	neg := a.Before(b)
	if neg {
		a, b = b, a
	}
	sec := a.Second() - b.Second()
	min := a.Minute() - b.Minute()
	hour := a.Hour() - b.Hour()
	day := a.Day() - b.Day()
	month := int(a.Month()) - int(b.Month())
	year := a.Year() - b.Year()

	if sec < 0 {
		sec += 60
		min--
	}
	if min < 0 {
		min += 60
		hour--
	}
	if hour < 0 {
		hour += 24
		day--
	}
	if day < 0 {
		// Borrow the number of days in the month preceding a.
		day += daysInMonth(a.Year(), int(a.Month())-1)
		month--
	}
	if month < 0 {
		month += 12
		year--
	}

	var parts []string
	add := func(n int, unit string) {
		if n != 0 {
			s := unit
			if n != 1 && n != -1 {
				s += "s"
			}
			parts = append(parts, fmt.Sprintf("%d %s", n, s))
		}
	}
	sign := 1
	if neg {
		sign = -1
	}
	add(sign*year, "year")
	// Postgres abbreviates months as "mon"/"mons".
	if month != 0 {
		u := "mon"
		if month != 1 {
			u += "s"
		}
		parts = append(parts, fmt.Sprintf("%d %s", sign*month, u))
	}
	add(sign*day, "day")
	if hour != 0 || min != 0 || sec != 0 {
		parts = append(parts, fmt.Sprintf("%02d:%02d:%02d", sign*hour, min, sec))
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}

// daysInMonth returns the number of days in month m of year y (m may be 0 or 13,
// wrapping to the adjacent year).
func daysInMonth(y, m int) int {
	return time.Date(y, time.Month(m)+1, 0, 0, 0, 0, 0, time.UTC).Day()
}
