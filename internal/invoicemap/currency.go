package invoicemap

import (
	"fmt"
	"math/big"
	"strings"
)

// zeroDecimalCurrencies is Stripe's published list of currencies whose
// "smallest unit" equals the currency unit (no minor units). Amounts on a
// Stripe invoice in these currencies are already in whole units.
var zeroDecimalCurrencies = map[string]bool{
	"bif": true, "clp": true, "djf": true, "gnf": true, "jpy": true,
	"kmf": true, "krw": true, "mga": true, "pyg": true, "rwf": true,
	"ugx": true, "vnd": true, "vuv": true, "xaf": true, "xof": true,
	"xpf": true,
}

// currencyMinorScale returns the multiplier to convert Stripe minor units
// into whole units for the given ISO currency. Returns 1 for zero-decimal
// currencies, 100 for everything else.
func currencyMinorScale(code string) int64 {
	if zeroDecimalCurrencies[strings.ToLower(code)] {
		return 1
	}
	return 100
}

// amountFromMinor converts an int64 Stripe amount in minor units to a
// big.Rat in whole currency units.
func amountFromMinor(minor int64, code string) *big.Rat {
	return new(big.Rat).SetFrac(big.NewInt(minor), big.NewInt(currencyMinorScale(code)))
}

// formatAmount renders a big.Rat with the appropriate number of decimal
// places for the given currency. For zero-decimal currencies the value
// must be integral; otherwise we render with 2 decimals (NAV's required
// precision for invoice monetary fields).
func formatAmount(r *big.Rat, code string) string {
	if zeroDecimalCurrencies[strings.ToLower(code)] {
		// Round to integer (half-away-from-zero) and emit a bare integer.
		n := roundToInt(r)
		return n.String()
	}
	return r.FloatString(2)
}

// formatHUF renders a big.Rat as a bare HUF integer (NAV stores HUF
// totals as integer Ft, no decimals).
func formatHUF(r *big.Rat) string {
	return roundToInt(r).String()
}

func roundToInt(r *big.Rat) *big.Int {
	// half-away-from-zero rounding using rat arithmetic
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	q, rem := new(big.Int).QuoRem(num, den, new(big.Int))
	if rem.Sign() == 0 {
		return q
	}
	// Multiply remainder*2 to compare to denominator.
	rem2 := new(big.Int).Lsh(rem, 1)
	cmp := rem2.CmpAbs(den)
	if cmp < 0 {
		return q
	}
	one := big.NewInt(1)
	if rem.Sign() < 0 {
		one.Neg(one)
	}
	return q.Add(q, one)
}

// toHUF multiplies a foreign-currency amount by the exchange rate. The
// rate is expressed as foreign→HUF (i.e. 1 EUR = rate HUF).
func toHUF(amount *big.Rat, rate *big.Rat) *big.Rat {
	return new(big.Rat).Mul(amount, rate)
}

// parseRate parses an exchange rate from string or float into a big.Rat.
func parseRate(s string) (*big.Rat, error) {
	r := new(big.Rat)
	if _, ok := r.SetString(s); !ok {
		return nil, fmt.Errorf("mapping: cannot parse exchange rate %q", s)
	}
	return r, nil
}
