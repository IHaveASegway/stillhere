package sqlite

import (
	"database/sql"
	"fmt"
	"math"
	"time"
)

// How domain values become column values and back, by the rules at the top
// of migrations/0001_facts.sql. Each encode has its decode next to it, so
// a reader can check the pair for a lossless round trip.

// timeLayout is RFC 3339 in UTC with exactly nine fractional digits. The
// fixed width is what makes the text sort like the time it encodes; with
// the trailing zeros trimmed, as time.RFC3339Nano does, "00.5Z" would sort
// before "00Z".
const timeLayout = "2006-01-02T15:04:05.000000000Z"

// encodeTime returns t as facts.db stores it: UTC text in timeLayout, or nil
// (NULL) for the zero time. The text has a four-digit year, so a time
// outside the years 0000 to 9999 is an error here rather than a row that
// would fail to load.
func encodeTime(t time.Time) (any, error) {
	if t.IsZero() {
		return nil, nil
	}
	u := t.UTC()
	if y := u.Year(); y < 0 || y > 9999 {
		return nil, fmt.Errorf("time %s is outside the years 0000 to 9999 that facts.db stores", t.Format(time.RFC3339Nano))
	}
	return u.Format(timeLayout), nil
}

// encodeTimes encodes each time with encodeTime, stopping at the first one
// that facts.db cannot store.
func encodeTimes(ts ...time.Time) ([]any, error) {
	vs := make([]any, len(ts))
	for i, t := range ts {
		v, err := encodeTime(t)
		if err != nil {
			return nil, err
		}
		vs[i] = v
	}
	return vs, nil
}

// decodeTime is the inverse of encodeTime. The time it returns is in UTC,
// so it is reflect.DeepEqual to the UTC time that was put.
func decodeTime(v sql.NullString) (time.Time, error) {
	if !v.Valid {
		return time.Time{}, nil
	}
	t, err := time.Parse(timeLayout, v.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("stored time %q: %w", v.String, err)
	}
	return t, nil
}

// encodeFloat returns f, or nil (NULL) for NaN, which SQLite cannot store:
// it would turn a NaN into NULL anyway, and a NOT NULL column would then
// reject the row.
func encodeFloat(f float64) any {
	if math.IsNaN(f) {
		return nil
	}
	return f
}

// decodeFloat is the inverse of encodeFloat.
func decodeFloat(v sql.NullFloat64) float64 {
	if !v.Valid {
		return math.NaN()
	}
	return v.Float64
}

// encodeBool returns 1 for true and 0 for false. database/sql scans them
// back into a bool.
func encodeBool(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// encodeBytes returns b, or nil (NULL) when it is empty: the core's empty
// slice is nil (docs/spec/codebase.md#determinism), and a NULL BLOB scans
// back as a nil slice.
func encodeBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
