package pg

import (
	"encoding/hex"
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
// string, and anything else becomes its string form. Arrays are converted
// element by element.
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

// numericValue renders a pgtype.Numeric as a JSON number when it is finite,
// and as a string ("NaN", "Infinity", "-Infinity") when it is not, because
// JSON has no representation for those.
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
	// value = Int * 10^Exp
	f := new(big.Float).SetInt(n.Int)
	if n.Exp != 0 {
		scale := new(big.Float).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs32(n.Exp))), nil))
		if n.Exp > 0 {
			f.Mul(f, scale)
		} else {
			f.Quo(f, scale)
		}
	}
	out, _ := f.Float64()
	return out
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

func formatUUID(b [16]byte) string {
	hexStr := hex.EncodeToString(b[:])
	return hexStr[0:8] + "-" + hexStr[8:12] + "-" + hexStr[12:16] + "-" + hexStr[16:20] + "-" + hexStr[20:32]
}

func formatInterval(iv pgtype.Interval) string {
	parts := []string{}
	if iv.Months != 0 {
		parts = append(parts, strconv.Itoa(int(iv.Months))+" mons")
	}
	if iv.Days != 0 {
		parts = append(parts, strconv.Itoa(int(iv.Days))+" days")
	}
	if iv.Microseconds != 0 {
		d := time.Duration(iv.Microseconds) * time.Microsecond
		parts = append(parts, d.String())
	}
	if len(parts) == 0 {
		return "00:00:00"
	}
	return strings.Join(parts, " ")
}
