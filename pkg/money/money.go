// Package money represents amounts as integer minor units + ISO currency.
//
// Money is always stored as int64 minor units (e.g. paise) plus a currency
// code; floating point is never used. Operations on mismatched currencies
// return an error rather than silently coercing.
package money

import (
	"errors"
	"fmt"
	"math"
)

// ErrCurrencyMismatch is returned when combining amounts of different currencies.
var ErrCurrencyMismatch = errors.New("money: currency mismatch")

// Money is an amount in integer minor units of Currency (e.g. 24000 paise, "INR").
type Money struct {
	Minor    int64
	Currency string
}

// New constructs a Money value.
func New(minor int64, ccy string) Money {
	return Money{Minor: minor, Currency: ccy}
}

// Add returns the sum of m and o, or ErrCurrencyMismatch if currencies differ.
func (m Money) Add(o Money) (Money, error) {
	if m.Currency != o.Currency {
		return Money{}, fmt.Errorf("%w: %s + %s", ErrCurrencyMismatch, m.Currency, o.Currency)
	}
	return Money{Minor: m.Minor + o.Minor, Currency: m.Currency}, nil
}

// Pct returns p percent of m, rounded half-away-from-zero to the nearest minor unit.
func (m Money) Pct(p float64) Money {
	v := float64(m.Minor) * p / 100.0
	return Money{Minor: int64(math.Round(v)), Currency: m.Currency}
}

// String renders the amount with a currency symbol and two decimal places,
// e.g. New(24000, "INR").String() == "₹240.00".
func (m Money) String() string {
	sym := symbol(m.Currency)
	neg := m.Minor < 0
	abs := m.Minor
	if neg {
		abs = -abs
	}
	major := abs / 100
	minor := abs % 100
	sign := ""
	if neg {
		sign = "-"
	}
	return fmt.Sprintf("%s%s%d.%02d", sign, sym, major, minor)
}

func symbol(ccy string) string {
	switch ccy {
	case "INR":
		return "₹"
	case "USD":
		return "$"
	case "EUR":
		return "€"
	case "GBP":
		return "£"
	default:
		return ccy + " "
	}
}
