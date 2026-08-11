package order

import (
	"strconv"
	"testing"
)

func TestRoundToTick(t *testing.T) {
	cases := []struct {
		name    string
		in      float64
		wantStr string
	}{
		{"100~1000원대, 아래로 스냅", 632.732, "632.7"},
		{"100~1000원대, 위로 스냅", 635.268, "635.3"},
		{"이미 tick의 배수", 634, "634.0"},
		{"200만원 이상, tick=1000", 91_000_123, "91000000"},
		{"100만~200만원, tick=500", 1_234_567, "1234500"},
		{"1만~10만원, tick=10, 반내림/반올림 경계(.5)", 72345, "72350"},
		{"10만~50만원, tick=50", 123456.7, "123450"},
		{"1~10원, tick=0.001", 7.1234, "7.123"},
		{"0.01~0.1원, tick=0.00001", 0.05678, "0.05678"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, decimals := RoundToTick(c.in)
			gotStr := strconv.FormatFloat(got, 'f', decimals, 64)
			if gotStr != c.wantStr {
				t.Errorf("RoundToTick(%v) = %s, want %s", c.in, gotStr, c.wantStr)
			}
		})
	}
}

func TestRoundToTickIsIdempotent(t *testing.T) {
	// 이미 tick에 맞는 가격을 다시 반올림해도 값이 안 바뀌어야 한다.
	price, decimals := RoundToTick(634)
	rounded, _ := RoundToTick(price)
	if rounded != price {
		t.Errorf("RoundToTick이 멱등하지 않음: %v -> %v", price, rounded)
	}
	if decimals != 1 {
		t.Errorf("100~1000원대는 소수 1자리를 기대했으나 %d", decimals)
	}
}
