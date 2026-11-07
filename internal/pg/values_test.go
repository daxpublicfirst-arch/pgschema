package pg

import (
	"math"
	"math/big"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

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
		{pgtype.Numeric{Int: big.NewInt(1250), Exp: -2, Valid: true}, float64(12.5)},
		{pgtype.Numeric{Int: big.NewInt(7), Exp: 0, Valid: true}, float64(7)},
		{pgtype.Numeric{Valid: false}, nil},
		{pgtype.Numeric{NaN: true, Valid: true}, "NaN"},
		{pgtype.Numeric{InfinityModifier: pgtype.Infinity, Valid: true}, "+Infinity"},
		{pgtype.Interval{Months: 1, Days: 2, Microseconds: int64(3*time.Second/time.Microsecond) + 500000, Valid: true}, "1 mons 2 days 3.5s"},
		{pgtype.Interval{Valid: true}, "00:00:00"},
		{pgtype.Interval{Valid: false}, nil},
	}
	for _, c := range cases {
		got := convertValue(c.in)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("convertValue(%#v) = %#v (%T), want %#v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}
