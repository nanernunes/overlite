package engine

import (
	"database/sql/driver"
	"fmt"
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

// pgToGoLayout translates the common Postgres to_char format tokens to a Go
// reference layout. It is intentionally partial (the full set is large).
var pgToGoLayout = strings.NewReplacer(
	"YYYY", "2006", "YY", "06",
	"Month", "January", "Mon", "Jan", "MM", "01",
	"Day", "Monday", "Dy", "Mon", "DD", "02",
	"HH24", "15", "HH12", "03", "HH", "03",
	"MI", "04", "SS", "05",
	"AM", "PM", "PM", "PM", "am", "pm", "pm", "pm",
	"TZ", "MST",
)

// toChar implements a common subset of Postgres to_char(ts, fmt).
func toChar(t time.Time, format string) string {
	return t.Format(pgToGoLayout.Replace(format))
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
