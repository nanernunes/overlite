package postgres

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"overlite/core"
)

const (
	oidUnknown = 705
	oidFloat4  = 700
)

// decodeTypedParam reads a binary parameter by its type, into the value the
// same parameter sent as text would have stored. ok is false for a type it
// does not know, which is then guessed by its length.
func decodeTypedParam(oid uint32, raw []byte) (core.Value, bool) {
	switch oid {
	case oidText, oidVarchar, oidBpchar, oidName, oidUnknown, oidJSON:
		return string(raw), true
	case oidJSONB:
		if len(raw) > 0 && raw[0] == 1 { // the jsonb format version
			return string(raw[1:]), true
		}
	case oidBool:
		if len(raw) == 1 {
			return raw[0] != 0, true
		}
	case oidInt2:
		if len(raw) == 2 {
			return int64(int16(byteOrder.Uint16(raw))), true
		}
	case oidInt4:
		if len(raw) == 4 {
			return int64(int32(byteOrder.Uint32(raw))), true
		}
	case oidOID:
		if len(raw) == 4 {
			return int64(byteOrder.Uint32(raw)), true
		}
	case oidInt8:
		if len(raw) == 8 {
			return int64(byteOrder.Uint64(raw)), true
		}
	case oidFloat4:
		if len(raw) == 4 {
			return float64(math.Float32frombits(byteOrder.Uint32(raw))), true
		}
	case oidFloat8:
		if len(raw) == 8 {
			return math.Float64frombits(byteOrder.Uint64(raw)), true
		}
	case oidTimestamp, oidTimestamptz:
		// timestamptz is the instant in UTC, stored bare as a literal with a
		// zone would be; timestamp is the wall clock as given.
		if len(raw) == 8 {
			switch us := int64(byteOrder.Uint64(raw)); us {
			case math.MaxInt64:
				return "infinity", true
			case math.MinInt64:
				return "-infinity", true
			default:
				return pgEpoch.Add(time.Duration(us) * time.Microsecond).Format("2006-01-02 15:04:05.999999"), true
			}
		}
	case oidDate:
		if len(raw) == 4 {
			return pgEpoch.AddDate(0, 0, int(int32(byteOrder.Uint32(raw)))).Format("2006-01-02"), true
		}
	case oidUUID:
		if len(raw) == 16 {
			return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16]), true
		}
	default:
		if isArrayOID(oid) || oid == oidNameArr || oid == oidOIDArr {
			return decodeArrayParam(raw)
		}
	}
	return nil, false
}

// decodeArrayParam reads a one-dimensional binary array into the JSON array
// an array is stored as.
func decodeArrayParam(raw []byte) (core.Value, bool) {
	r := newReader(raw)
	ndim := r.int32()
	r.int32() // has-nulls flag
	elem := uint32(r.int32())
	if r.err != nil || ndim > 1 {
		return nil, false
	}
	n := 0
	if ndim == 1 {
		n = r.int32()
		r.int32() // lower bound
	}
	out := make([]any, 0, max(n, 0))
	for i := 0; i < n; i++ {
		length := r.int32()
		if length == -1 {
			out = append(out, nil)
			continue
		}
		v, ok := decodeTypedParam(elem, r.bytes(length))
		if !ok {
			return nil, false
		}
		out = append(out, v)
	}
	if r.err != nil {
		return nil, false
	}
	b, err := json.Marshal(out)
	if err != nil {
		return nil, false
	}
	return string(b), true
}
