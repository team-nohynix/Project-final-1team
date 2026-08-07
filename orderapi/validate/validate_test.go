package validate

import "testing"

func TestIsValidTickPrice(t *testing.T) {
	cases := []struct {
		name  string
		price float64
		want  bool
	}{
		{"100~1000원대, tick=0.1 배수", 632.7, true},
		{"100~1000원대, tick=0.1 배수 아님", 632.73, false},
		{"200만원 이상, tick=1000 배수", 91_000_000, true},
		{"200만원 이상, tick=1000 배수 아님", 91_000_123, false},
		{"100만~200만원, tick=500 배수", 1_234_500, true},
		{"100만~200만원, tick=500 배수 아님", 1_234_567, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isValidTickPrice(c.price); got != c.want {
				t.Errorf("isValidTickPrice(%v) = %v, want %v", c.price, got, c.want)
			}
		})
	}
}

func TestValidateSide(t *testing.T) {
	if _, _, ok := ValidateSide("BUY"); !ok {
		t.Error("BUY는 유효해야 함")
	}
	if _, _, ok := ValidateSide("SELL"); !ok {
		t.Error("SELL은 유효해야 함")
	}
	code, _, ok := ValidateSide("HOLD")
	if ok || code != "INVALID_SIDE" {
		t.Errorf("HOLD는 INVALID_SIDE여야 하는데 ok=%v code=%q", ok, code)
	}
}

func TestValidatePrice(t *testing.T) {
	if _, _, _, ok := ValidatePrice("KRW-BTC", "71500000"); !ok {
		t.Error("71500000(1000원 배수)은 유효해야 함")
	}
	if _, code, _, ok := ValidatePrice("KRW-BTC", "71500123"); ok || code != "INVALID_PRICE_UNIT" {
		t.Errorf("호가 단위 배수가 아니면 INVALID_PRICE_UNIT이어야 하는데 ok=%v code=%q", ok, code)
	}
	if _, code, _, ok := ValidatePrice("KRW-BTC", "0"); ok || code != "INVALID_PRICE" {
		t.Errorf("0은 INVALID_PRICE여야 하는데 ok=%v code=%q", ok, code)
	}
	if _, code, _, ok := ValidatePrice("KRW-BTC", "abc"); ok || code != "INVALID_PRICE" {
		t.Errorf("숫자가 아니면 INVALID_PRICE여야 하는데 ok=%v code=%q", ok, code)
	}
}

func TestValidateQuantity(t *testing.T) {
	if _, _, _, ok := ValidateQuantity("0.015"); !ok {
		t.Error("0.015는 유효해야 함")
	}
	if _, code, _, ok := ValidateQuantity("0"); ok || code != "INVALID_QUANTITY" {
		t.Errorf("0은 INVALID_QUANTITY여야 하는데 ok=%v code=%q", ok, code)
	}
	if _, code, _, ok := ValidateQuantity("-1"); ok || code != "INVALID_QUANTITY" {
		t.Errorf("음수는 INVALID_QUANTITY여야 하는데 ok=%v code=%q", ok, code)
	}
}

func TestIsTargetMarket(t *testing.T) {
	if !IsTargetMarket("KRW-BTC") {
		t.Error("KRW-BTC는 대상 마켓이어야 함")
	}
	if IsTargetMarket("KRW-NOTREAL") {
		t.Error("KRW-NOTREAL은 대상 마켓이 아니어야 함")
	}
}
