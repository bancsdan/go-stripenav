package invoicemap

import (
	"math/big"
	"testing"
)

func TestCurrencyMinorScale(t *testing.T) {
	if currencyMinorScale("HUF") != 100 {
		t.Errorf("HUF scale should be 100 (Stripe stores HUF amounts in 'fillér' minor units)")
	}
	if currencyMinorScale("usd") != 100 {
		t.Errorf("USD scale should be 100")
	}
	if currencyMinorScale("JPY") != 1 {
		t.Errorf("JPY (zero-decimal) scale should be 1")
	}
	if currencyMinorScale("KRW") != 1 {
		t.Errorf("KRW (zero-decimal) scale should be 1")
	}
}

func TestAmountFromMinor(t *testing.T) {
	got := amountFromMinor(12345, "EUR")
	want := new(big.Rat).SetFrac(big.NewInt(12345), big.NewInt(100))
	if got.Cmp(want) != 0 {
		t.Fatalf("amountFromMinor(12345, EUR) = %s want %s", got, want)
	}
	got = amountFromMinor(123, "JPY")
	if got.Cmp(big.NewRat(123, 1)) != 0 {
		t.Fatalf("amountFromMinor(123, JPY) = %s want 123", got)
	}
}

func TestFormatAmount(t *testing.T) {
	if s := formatAmount(big.NewRat(12345, 100), "EUR"); s != "123.45" {
		t.Errorf("EUR format = %q want 123.45", s)
	}
	if s := formatAmount(big.NewRat(123, 1), "JPY"); s != "123" {
		t.Errorf("JPY format = %q want 123", s)
	}
	// 0.5 rounds away from zero for zero-decimal currencies.
	if s := formatAmount(big.NewRat(1, 2), "JPY"); s != "1" {
		t.Errorf("JPY 0.5 rounding = %q want 1", s)
	}
}

func TestRoundToInt(t *testing.T) {
	cases := []struct {
		in   *big.Rat
		want int64
	}{
		{big.NewRat(5, 2), 3},      // 2.5 -> 3
		{big.NewRat(-5, 2), -3},    // -2.5 -> -3
		{big.NewRat(7, 4), 2},      // 1.75 -> 2
		{big.NewRat(1, 4), 0},      // 0.25 -> 0
		{big.NewRat(0, 1), 0},
	}
	for _, c := range cases {
		got := roundToInt(c.in)
		if got.Int64() != c.want {
			t.Errorf("roundToInt(%s) = %d want %d", c.in, got.Int64(), c.want)
		}
	}
}

func TestToHUF(t *testing.T) {
	amount := big.NewRat(100, 1) // 100 EUR
	rate := big.NewRat(40000, 100) // 400 HUF / EUR
	got := toHUF(amount, rate)
	if got.Cmp(big.NewRat(40000, 1)) != 0 {
		t.Fatalf("toHUF = %s want 40000", got)
	}
}
