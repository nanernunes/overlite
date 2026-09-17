package postgres

import (
	"strings"
	"time"

	"overlite/core"
)

// A client that asks for binary results gets whatever the row description
// promised, so a date or timestamp column has to be encoded the way Postgres
// does: microseconds (or days) relative to 2000-01-01, not its text spelling.
// Sending the text under a binary OID leaves the client decoding 27 bytes as an
// 8-byte integer.

// pgEpoch is the moment Postgres counts its binary timestamps from.
var pgEpoch = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// timestampLayouts covers the spellings SQLite stores a time value in.
var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05.999999999",
	"15:04:05",
}

// parseStoredTime reads a stored timestamp, date or time value.
func parseStoredTime(v core.Value) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, true
	case string:
		s := strings.TrimSpace(t)
		for _, layout := range timestampLayouts {
			if tm, err := time.Parse(layout, s); err == nil {
				return tm, true
			}
		}
	}
	return time.Time{}, false
}

// encodeBinaryTimestamp renders a timestamp as microseconds since pgEpoch.
func encodeBinaryTimestamp(tm time.Time) []byte {
	micros := tm.UTC().Sub(pgEpoch).Microseconds()
	return appendUint64(nil, uint64(micros))
}

// encodeBinaryDate renders a date as days since pgEpoch.
func encodeBinaryDate(tm time.Time) []byte {
	days := int32(tm.UTC().Truncate(24*time.Hour).Sub(pgEpoch) / (24 * time.Hour))
	return appendUint32(nil, uint32(days))
}

// encodeBinaryTime renders a time of day as microseconds since midnight.
func encodeBinaryTime(tm time.Time) []byte {
	midnight := time.Date(tm.Year(), tm.Month(), tm.Day(), 0, 0, 0, 0, tm.Location())
	return appendUint64(nil, uint64(tm.Sub(midnight).Microseconds()))
}

// isTimeOID reports whether a column holds a date or time value.
func isTimeOID(oid uint32) bool {
	switch oid {
	case oidDate, oidTime, oidTimestamp, oidTimestamptz:
		return true
	}
	return false
}

// canonicalTimeText renders a stored time value the way Postgres writes it.
// A value it cannot parse is passed through: better the client's own complaint
// than a value invented here.
func canonicalTimeText(oid uint32, s string) string {
	tm, ok := parseStoredTime(s)
	if !ok {
		// Not a full timestamp, but it may still be a bare one that only needs
		// its offset spelled out.
		if oid == oidTimestamptz {
			return withUTCOffset(s)
		}
		return s
	}
	switch oid {
	case oidDate:
		return tm.Format("2006-01-02")
	case oidTime:
		return tm.Format("15:04:05.999999")
	case oidTimestamptz:
		return tm.UTC().Format("2006-01-02 15:04:05.999999-07")
	default:
		return tm.Format("2006-01-02 15:04:05.999999")
	}
}
