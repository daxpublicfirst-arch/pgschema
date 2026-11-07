package pg

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/big"
	"net"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// convertValue turns a pgx-decoded row value into a JSON-friendly value:
// numbers stay numbers, NULL stays null, booleans stay booleans, strings
// stay strings, timestamps become RFC 3339 strings, bytea becomes a hex
// string, JSON objects stay objects, UUIDs become their canonical string
// form, and anything else becomes its string form. Arrays are converted
// element by element.
//
// NOTE: when pgx scans into `any`, several types do NOT arrive as the
// pgtype struct the name suggests: uuid arrives as [16]byte, date arrives
// as time.Time (see collectRows, which formats it by OID), and json/jsonb
// arrive as map[string]any / []any. The cases below handle the types pgx
// actually produces, not just the pgtype structs.
func convertValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case bool:
		return t
	case string:
		return t
	case int16:
		return int64(t)
	case int32:
		return int64(t)
	case int64:
		return t
	case int:
		return int64(t)
	case uint16:
		return uint64(t)
	case uint32:
		return uint64(t)
	case uint64:
		return t
	case float32:
		return floatOrString(float64(t))
	case float64:
		return floatOrString(t)
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case []byte:
		return hex.EncodeToString(t)
	case [16]byte:
		// pgx decodes uuid into [16]byte when scanning into any.
		return formatUUID(t)
	case pgtype.Numeric:
		return numericValue(t)
	case pgtype.Date:
		if !t.Valid {
			return nil
		}
		return t.Time.Format("2006-01-02")
	case pgtype.Timestamp:
		if !t.Valid {
			return nil
		}
		return t.Time.Format(time.RFC3339Nano)
	case pgtype.Timestamptz:
		if !t.Valid {
			return nil
		}
		return t.Time.Format(time.RFC3339Nano)
	case pgtype.Time:
		if !t.Valid {
			return nil
		}
		return formatTimeOfDay(t.Microseconds)
	case pgtype.UUID:
		if !t.Valid {
			return nil
		}
		return formatUUID(t.Bytes)
	case pgtype.Interval:
		if !t.Valid {
			return nil
		}
		return formatInterval(t)
	case net.IPNet:
		return t.String()
	case *net.IPNet:
		if t == nil {
			return nil
		}
		return t.String()
	case net.IP:
		return t.String()
	}

	// Arrays (text[], int[] ...) come back as typed slices; convert each
	// element. []byte is handled above, so it never reaches here.
	rv := reflect.ValueOf(v)
	if rv.IsValid() && (rv.Kind() == reflect.Slice || rv.Kind() == reflect.Array) {
		out := make([]any, 0, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			out = append(out, convertValue(rv.Index(i).Interface()))
		}
		return out
	}

	// JSON objects (json/jsonb) come back as maps; convert each value so
	// nested objects and arrays keep their structure instead of being
	// flattened by fmt.Sprintf("%v").
	if rv.IsValid() && rv.Kind() == reflect.Map {
		out := make(map[string]any, rv.Len())
		iter := rv.MapRange()
		for iter.Next() {
			out[fmt.Sprintf("%v", iter.Key().Interface())] = convertValue(iter.Value().Interface())
		}
		return out
	}

	// Pointers (e.g. *string inside a decoded map): dereference, nil stays nil.
	if rv.IsValid() && rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		return convertValue(rv.Elem().Interface())
	}

	// Last resort: a String() method, otherwise %v.
	if s, ok := v.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%v", v)
}

func floatOrString(f float64) any {
	if math.IsNaN(f) {
		return "NaN"
	}
	if math.IsInf(f, 1) {
		return "+Infinity"
	}
	if math.IsInf(f, -1) {
		return "-Infinity"
	}
	return f
}

// numericValue renders a pgtype.Numeric as an exact JSON number when it
// is finite, and as a string ("NaN", "Infinity", "-Infinity") when it is
// not, because JSON has no representation for those. It returns
// json.Number, not float64: converting through float64 silently rounds
// values with more than ~15 significant digits (e.g. numeric(32,2)
// 123456789012345678901234567890.12 became 1.2345678901234568e+29).
// json.Number is emitted verbatim by encoding/json, so the JSON output
// keeps the exact decimal digits.
func numericValue(n pgtype.Numeric) any {
	if !n.Valid {
		return nil
	}
	if n.NaN {
		return "NaN"
	}
	switch n.InfinityModifier {
	case pgtype.Infinity:
		return "+Infinity"
	case pgtype.NegativeInfinity:
		return "-Infinity"
	}
	if n.Int == nil {
		return nil
	}
	return json.Number(numericString(n.Int, n.Exp))
}

// numericString renders value = digits * 10^exp as an exact decimal
// string, with no trailing zeros in the fraction and no exponent.
func numericString(digits *big.Int, exp int32) string {
	s := digits.String()
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	prefix := ""
	if neg {
		prefix = "-"
	}
	if s == "0" {
		return "0"
	}
	if exp >= 0 {
		return prefix + s + strings.Repeat("0", int(exp))
	}
	point := int(-exp)
	if len(s) > point {
		intPart := s[:len(s)-point]
		fracPart := strings.TrimRight(s[len(s)-point:], "0")
		if fracPart == "" {
			return prefix + intPart
		}
		return prefix + intPart + "." + fracPart
	}
	fracPart := strings.TrimRight(strings.Repeat("0", point-len(s))+s, "0")
	if fracPart == "" {
		return "0"
	}
	return prefix + "0." + fracPart
}

// formatTimeOfDay renders a PostgreSQL time (microseconds since midnight)
// as "HH:MM:SS" with a fractional part only when needed. 24:00:00 is kept
// as 24:00:00 rather than rolling over to the next day.
func formatTimeOfDay(microseconds int64) string {
	us := microseconds % 1000000
	totalSeconds := microseconds / 1000000
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60
	base := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if us == 0 {
		return base
	}
	frac := strings.TrimRight(fmt.Sprintf("%06d", us), "0")
	return base + "." + frac
}

func formatUUID(b [16]byte) string {
	hexStr := hex.EncodeToString(b[:])
	return hexStr[0:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:32]
}

// formatInterval renders a PostgreSQL interval the way PostgreSQL
// itself renders it with the default (postgres) IntervalStyle:
// years/months/days plus HH:MM:SS, e.g. "1 year 2 mons 3 days
// 04:05:06.789", "1 day 02:00:00", "00:00:00" for zero.
//
// The rules mirror interval_out in the server, verified against
// PostgreSQL 16.15:
//   - Months splits into years (Months/12, truncating toward zero)
//     and remaining months; each part is rendered only when non-zero.
//   - Each component keeps its own sign: "1 day -02:00:00" and
//     "-1 years -2 mons" are real outputs. A component of -1 takes the
//     plural ("-1 years", "-1 mons", "-1 days") because the server
//     pluralises on `value != 1`, not on the absolute value.
//   - The time part is HH:MM:SS with hours unclamped (25:00:00 is
//     possible), a leading "-" on the whole time when the microseconds
//     are negative ("-00:30:00"), and a fractional part with trailing
//     zeros trimmed (".5", ".789", ".000001") only when non-zero.
//   - The time part is omitted when the microseconds are zero, except
//     that an all-zero interval renders as "00:00:00".
func formatInterval(iv pgtype.Interval) string {
	parts := []string{}
	years := iv.Months / 12
	mons := iv.Months % 12
	if years != 0 {
		parts = append(parts, strconv.Itoa(int(years))+plural(" year", years))
	}
	if mons != 0 {
		parts = append(parts, strconv.Itoa(int(mons))+plural(" mon", mons))
	}
	if iv.Days != 0 {
		parts = append(parts, strconv.Itoa(int(iv.Days))+plural(" day", iv.Days))
	}
	if iv.Microseconds != 0 {
		parts = append(parts, formatIntervalTime(iv.Microseconds))
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}

// plural returns word + "s" unless n is exactly 1, matching
// PostgreSQL's interval_out (so -1 pluralises: "-1 years").
func plural(word string, n int32) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// formatIntervalTime renders the time part of an interval:
// HH:MM:SS with unclamped hours, a "-" prefix when negative, and a
// fractional part (trailing zeros trimmed) only when non-zero.
func formatIntervalTime(microseconds int64) string {
	neg := microseconds < 0
	abs := microseconds
	if neg {
		abs = -abs
	}
	totalSeconds := abs / 1000000
	frac := abs % 1000000
	h := totalSeconds / 3600
	m := (totalSeconds % 3600) / 60
	s := totalSeconds % 60
	base := fmt.Sprintf("%02d:%02d:%02d", h, m, s)
	if neg {
		base = "-" + base
	}
	if frac == 0 {
		return base
	}
	return base + "." + strings.TrimRight(fmt.Sprintf("%06d", frac), "0")
}
