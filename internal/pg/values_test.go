package pg

import (
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func mustBigInt(s string) *big.Int {
	n, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int literal: " + s)
	}
	return n
}

func TestConvertValue(t *testing.T) {
	ts := time.Date(2026, 11, 8, 2, 30, 0, 123000000, time.UTC)
	cases := []struct {
		in   any
		want any
	}{
		{nil, nil},
		{true, true},
		{"hello", "hello"},
		{int16(7), int64(7)},
		{int32(-3), int64(-3)},
		{int64(1234567890123), int64(1234567890123)},
		{uint32(9), uint64(9)},
		{float32(1.5), float64(1.5)},
		{float64(2.5), float64(2.5)},
		{float64(math.NaN()), "NaN"},
		{float64(math.Inf(1)), "+Infinity"},
		{ts, "2026-11-08T02:30:00.123Z"},
		{[]byte{0xde, 0xad, 0xbe, 0xef}, "deadbeef"},
		{[]byte{}, ""},
		{[]string{"a", "b"}, []any{"a", "b"}},
		{[]int32{1, 2}, []any{int64(1), int64(2)}},
		{pgtype.Date{Time: time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC), Valid: true}, "2026-01-02"},
		{pgtype.Date{Valid: false}, nil},
		{pgtype.Timestamp{Time: ts, Valid: true}, "2026-11-08T02:30:00.123Z"},
		{pgtype.UUID{Bytes: [16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, Valid: true}, "00112233-4455-6677-8899-aabbccddeeff"},
		{pgtype.UUID{Valid: false}, nil},
		// pgx decodes uuid as [16]byte when scanning into any.
		{[16]byte{0x00, 0x11, 0x22, 0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff}, "00112233-4455-6677-8899-aabbccddeeff"},
		// pgx decodes time as pgtype.Time.
		{pgtype.Time{Microseconds: 37230000000, Valid: true}, "10:20:30"},
		{pgtype.Time{Microseconds: 37230500000, Valid: true}, "10:20:30.5"},
		{pgtype.Time{Microseconds: 37230000500, Valid: true}, "10:20:30.0005"},
		{pgtype.Time{Microseconds: 86400000000, Valid: true}, "24:00:00"},
		{pgtype.Time{Valid: false}, nil},
		// Numerics stay exact: json.Number, never float64.
		{pgtype.Numeric{Int: big.NewInt(1250), Exp: -2, Valid: true}, json.Number("12.5")},
		{pgtype.Numeric{Int: big.NewInt(7), Exp: 0, Valid: true}, json.Number("7")},
		{pgtype.Numeric{Int: big.NewInt(-1250), Exp: -2, Valid: true}, json.Number("-12.5")},
		{pgtype.Numeric{Int: big.NewInt(1), Exp: 2, Valid: true}, json.Number("100")},
		{pgtype.Numeric{Int: big.NewInt(0), Exp: -2, Valid: true}, json.Number("0")},
		{pgtype.Numeric{Int: big.NewInt(100), Exp: -2, Valid: true}, json.Number("1")},
		{pgtype.Numeric{Int: mustBigInt("12345678901234567890123456789012"), Exp: -2, Valid: true}, json.Number("123456789012345678901234567890.12")},
		{pgtype.Numeric{Valid: false}, nil},
		{pgtype.Numeric{NaN: true, Valid: true}, "NaN"},
		{pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, "+Infinity"},
		{pgtype.Interval{Months: 1, Days: 2, Microseconds: int64(3*time.Second/time.Microsecond) + 500000, Valid: true}, "1 mons 2 days 3.5s"},
		{pgtype.Interval{Valid: true}, "00:00:00"},
		{pgtype.Interval{Valid: false}, nil},
		// pgx decodes json/jsonb objects as map[string]any: the structure
		// must survive, not become a "map[...]" string.
		{map[string]any{"a": float64(1), "b": map[string]any{"c": []any{float64(1), float64(2), map[string]any{"d": "x"}}}}, map[string]any{"a": float64(1), "b": map[string]any{"c": []any{float64(1), float64(2), map[string]any{"d": "x"}}}}},
		{[]any{map[string]any{"k": "v"}, float64(2)}, []any{map[string]any{"k": "v"}, float64(2)}},
		{map[string]any{}, map[string]any{}},
		// Pointers inside decoded maps dereference; nil stays nil.
		{func() *string { s := "hi"; return &s }(), "hi"},
		{(*string)(nil), nil},
	}
	for _, c := range cases {
		got := convertValue(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("convertValue(%#v) = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}
