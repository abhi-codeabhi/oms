package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/promotions/internal/domain"
)

// fixedNow is the reference "current time" used across evaluation tests.
var fixedNow = time.Date(2026, 7, 1, 18, 30, 0, 0, time.UTC)

func inr(minor int64) money.Money { return money.New(minor, "INR") }

func mustCoupon(t *testing.T, in domain.NewCouponInput) domain.Coupon {
	t.Helper()
	c, err := domain.NewCoupon(in)
	if err != nil {
		t.Fatalf("NewCoupon(%+v): %v", in, err)
	}
	return c
}

func TestNewCoupon_Validation(t *testing.T) {
	tests := []struct {
		name    string
		in      domain.NewCouponInput
		wantErr error
	}{
		{"ok percent", domain.NewCouponInput{Code: "save10", Type: "percent", Value: 10, Active: true}, nil},
		{"ok flat", domain.NewCouponInput{Code: "flat50", Type: "flat", Value: 5000, Active: true}, nil},
		{"missing code", domain.NewCouponInput{Type: "percent", Value: 10}, domain.ErrInvalid},
		{"bad type", domain.NewCouponInput{Code: "X", Type: "bogus", Value: 10}, domain.ErrInvalid},
		{"zero value", domain.NewCouponInput{Code: "X", Type: "percent", Value: 0}, domain.ErrInvalid},
		{"percent over 100", domain.NewCouponInput{Code: "X", Type: "percent", Value: 150}, domain.ErrInvalid},
		{"negative min order", domain.NewCouponInput{Code: "X", Type: "flat", Value: 100, MinOrder: money.New(-1, "INR")}, domain.ErrInvalid},
		{"bad starts_at", domain.NewCouponInput{Code: "X", Type: "flat", Value: 100, StartsAt: "not-a-time"}, domain.ErrInvalid},
		{"window inverted", domain.NewCouponInput{Code: "X", Type: "flat", Value: 100, StartsAt: "2026-07-02T00:00:00Z", EndsAt: "2026-07-01T00:00:00Z"}, domain.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := domain.NewCoupon(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if c.Code == "" || c.Code != stringsUpper(tc.in.Code) {
				t.Fatalf("code not normalised: got %q", c.Code)
			}
		})
	}
}

func stringsUpper(s string) string { return domain.NormalizeCode(s) }

// TestCouponApply exercises every gate of the ported engine on a single coupon.
func TestCouponApply(t *testing.T) {
	win := domain.NewCouponInput{Code: "save20", Type: "percent", Value: 20, Active: true,
		StartsAt: "2026-07-01T09:00:00Z", EndsAt: "2026-07-01T23:00:00Z"}

	tests := []struct {
		name        string
		in          domain.NewCouponInput
		subtotal    money.Money
		category    string
		now         time.Time
		wantOK      bool
		wantMinor   int64
		wantReason  string
	}{
		{
			name:      "percent discount",
			in:        domain.NewCouponInput{Code: "save20", Type: "percent", Value: 20, Active: true},
			subtotal:  inr(100000), now: fixedNow, wantOK: true, wantMinor: 20000,
		},
		{
			name:      "flat discount",
			in:        domain.NewCouponInput{Code: "flat50", Type: "flat", Value: 5000, Active: true},
			subtotal:  inr(100000), now: fixedNow, wantOK: true, wantMinor: 5000,
		},
		{
			name:      "percent capped at subtotal",
			in:        domain.NewCouponInput{Code: "all", Type: "percent", Value: 100, Active: true},
			subtotal:  inr(30000), now: fixedNow, wantOK: true, wantMinor: 30000,
		},
		{
			name:      "flat capped at subtotal",
			in:        domain.NewCouponInput{Code: "big", Type: "flat", Value: 99999, Active: true},
			subtotal:  inr(20000), now: fixedNow, wantOK: true, wantMinor: 20000,
		},
		{
			name:       "inactive excluded",
			in:         domain.NewCouponInput{Code: "off", Type: "percent", Value: 20, Active: false},
			subtotal:   inr(100000), now: fixedNow, wantOK: false, wantReason: "inactive",
		},
		{
			name:       "min order gate blocks",
			in:         domain.NewCouponInput{Code: "min", Type: "flat", Value: 5000, Active: true, MinOrder: inr(50000)},
			subtotal:   inr(40000), now: fixedNow, wantOK: false, wantReason: "min_order_not_met",
		},
		{
			name:      "min order gate met",
			in:        domain.NewCouponInput{Code: "min", Type: "flat", Value: 5000, Active: true, MinOrder: inr(50000)},
			subtotal:  inr(50000), now: fixedNow, wantOK: true, wantMinor: 5000,
		},
		{
			name:      "category match applies",
			in:        domain.NewCouponInput{Code: "drinks", Type: "percent", Value: 10, Active: true, Category: "drinks"},
			subtotal:  inr(100000), category: "drinks", now: fixedNow, wantOK: true, wantMinor: 10000,
		},
		{
			name:       "category mismatch excluded",
			in:         domain.NewCouponInput{Code: "drinks", Type: "percent", Value: 10, Active: true, Category: "drinks"},
			subtotal:   inr(100000), category: "mains", now: fixedNow, wantOK: false, wantReason: "category_mismatch",
		},
		{
			name:      "within time window",
			in:        win,
			subtotal:  inr(100000), now: fixedNow, wantOK: true, wantMinor: 20000,
		},
		{
			name:       "before window not started",
			in:         win,
			subtotal:   inr(100000), now: time.Date(2026, 7, 1, 8, 0, 0, 0, time.UTC), wantOK: false, wantReason: "not_started",
		},
		{
			name:       "after window expired",
			in:         win,
			subtotal:   inr(100000), now: time.Date(2026, 7, 2, 0, 0, 0, 0, time.UTC), wantOK: false, wantReason: "expired",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := mustCoupon(t, tc.in)
			res := c.Apply(tc.subtotal, tc.category, tc.now)
			if res.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (reason=%q)", res.OK, tc.wantOK, res.Reason)
			}
			if tc.wantOK {
				if res.Discount.Minor != tc.wantMinor {
					t.Fatalf("discount = %d, want %d", res.Discount.Minor, tc.wantMinor)
				}
				if res.Discount.Currency != tc.subtotal.Currency {
					t.Fatalf("discount currency = %q, want %q", res.Discount.Currency, tc.subtotal.Currency)
				}
			} else if tc.wantReason != "" && res.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", res.Reason, tc.wantReason)
			}
		})
	}
}

// TestEvaluate_BestOfSelection covers the engine's best-of (non-stacking) selection
// across the catalogue, plus the coupon_code filter path.
func TestEvaluate_BestOfSelection(t *testing.T) {
	coupons := []domain.Coupon{
		mustCoupon(t, domain.NewCouponInput{Code: "small", Type: "percent", Value: 10, Active: true}), // 10000
		mustCoupon(t, domain.NewCouponInput{Code: "big", Type: "percent", Value: 25, Active: true}),   // 25000
		mustCoupon(t, domain.NewCouponInput{Code: "flat", Type: "flat", Value: 5000, Active: true}),   // 5000
		mustCoupon(t, domain.NewCouponInput{Code: "dead", Type: "percent", Value: 90, Active: false}), // excluded
	}
	subtotal := inr(100000)

	tests := []struct {
		name        string
		code        string
		category    string
		wantApplied string
		wantMinor   int64
	}{
		{"best of all (no code)", "", "", "BIG", 25000},
		{"specific code wins only itself", "small", "", "SMALL", 10000},
		{"specific code case-insensitive", "FlAt", "", "FLAT", 5000},
		{"inactive code yields nothing", "dead", "", "", 0},
		{"unknown code yields nothing", "nope", "", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := domain.Evaluate(coupons, subtotal, tc.code, tc.category, fixedNow)
			if ev.Applied != tc.wantApplied {
				t.Fatalf("applied = %q, want %q", ev.Applied, tc.wantApplied)
			}
			if ev.Discount.Minor != tc.wantMinor {
				t.Fatalf("discount = %d, want %d", ev.Discount.Minor, tc.wantMinor)
			}
			if ev.Discount.Currency != "INR" {
				t.Fatalf("currency = %q, want INR", ev.Discount.Currency)
			}
		})
	}
}

// TestEvaluate_CategoryRestrictedBestOf ensures a category-restricted coupon only
// wins when the cart category matches, otherwise a general coupon is chosen.
func TestEvaluate_CategoryRestrictedBestOf(t *testing.T) {
	coupons := []domain.Coupon{
		mustCoupon(t, domain.NewCouponInput{Code: "gen", Type: "percent", Value: 5, Active: true}),                       // 5000 any
		mustCoupon(t, domain.NewCouponInput{Code: "drink", Type: "percent", Value: 30, Active: true, Category: "drinks"}), // 30000 drinks only
	}
	subtotal := inr(100000)

	// drinks cart -> the bigger drinks-only coupon wins.
	ev := domain.Evaluate(coupons, subtotal, "", "drinks", fixedNow)
	if ev.Applied != "DRINK" || ev.Discount.Minor != 30000 {
		t.Fatalf("drinks cart: applied=%q discount=%d, want DRINK/30000", ev.Applied, ev.Discount.Minor)
	}

	// mains cart -> drinks coupon excluded, the general coupon wins.
	ev = domain.Evaluate(coupons, subtotal, "", "mains", fixedNow)
	if ev.Applied != "GEN" || ev.Discount.Minor != 5000 {
		t.Fatalf("mains cart: applied=%q discount=%d, want GEN/5000", ev.Applied, ev.Discount.Minor)
	}
}
