package erjet

import (
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Normalize maps one scanned pgx value to its JSON-friendly wire shape:
// pgtype.Numeric to float64, time.Time to RFC 3339 UTC, [16]byte (uuid) to
// the "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" string, integers
// (int16/int32/int64) to float64 - the same number the JSON decode of a
// payload yields, so both sides of the audit diff compare alike; nil passes
// through. Exported for reuse in application-side Entity.Load
// implementations.
func Normalize(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case time.Time:
		return x.UTC().Format(time.RFC3339)
	case pgtype.Numeric:
		f, err := x.Float64Value()
		if err != nil || !f.Valid {
			return nil
		}
		return f.Float64
	case [16]byte:
		return fmt.Sprintf("%x-%x-%x-%x-%x", x[0:4], x[4:6], x[6:8], x[8:10], x[10:16])
	case int16:
		return float64(x)
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return v
	}
}

// normalizeIdentifier keeps database identifiers in their string wire shape.
func normalizeIdentifier(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		s, _ := Normalize(v).(string)
		return s
	}
}
