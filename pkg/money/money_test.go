package money

import (
	"errors"
	"testing"
)

func TestAdd(t *testing.T) {
	tests := []struct {
		name    string
		a, b    Money
		want    int64
		wantErr error
	}{
		{"same currency", New(24000, "INR"), New(1000, "INR"), 25000, nil},
		{"negative", New(500, "INR"), New(-200, "INR"), 300, nil},
		{"mismatch", New(100, "INR"), New(100, "USD"), 0, ErrCurrencyMismatch},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.a.Add(tc.b)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Add error = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Add unexpected error: %v", err)
			}
			if got.Minor != tc.want {
				t.Fatalf("Add = %d, want %d", got.Minor, tc.want)
			}
			if got.Currency != tc.a.Currency {
				t.Fatalf("Add currency = %q, want %q", got.Currency, tc.a.Currency)
			}
		})
	}
}

func TestPct(t *testing.T) {
	tests := []struct {
		name string
		m    Money
		p    float64
		want int64
	}{
		{"18 pct GST of 240.00", New(24000, "INR"), 18, 4320},
		{"5 pct of 199", New(199, "INR"), 5, 10}, // 9.95 -> round 10
		{"half rounds away", New(101, "INR"), 50, 51}, // 50.5 -> 51
		{"zero", New(0, "INR"), 18, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.m.Pct(tc.p)
			if got.Minor != tc.want {
				t.Fatalf("Pct(%v) = %d, want %d", tc.p, got.Minor, tc.want)
			}
		})
	}
}

func TestString(t *testing.T) {
	tests := []struct {
		name string
		m    Money
		want string
	}{
		{"inr", New(24000, "INR"), "₹240.00"},
		{"inr paise", New(24050, "INR"), "₹240.50"},
		{"usd", New(1099, "USD"), "$10.99"},
		{"negative", New(-500, "INR"), "-₹5.00"},
		{"unknown ccy", New(100, "JPY"), "JPY 1.00"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
