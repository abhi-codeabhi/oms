package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/promotions/internal/app"
	"github.com/restorna/platform/services/promotions/internal/domain"
)

const rid = "out_01HXTESTRESTAURANT00000000"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 18, 30, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func inr(minor int64) money.Money { return money.New(minor, "INR") }

func newApp(repo *fakeRepo) *app.App { return app.New(repo, fixedClock()) }

func TestUpsertCoupon_PersistsAndEmitsEvent(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	c, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{
		Code: "save10", Type: "percent", Value: 10, Active: true,
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if c.Code != "SAVE10" {
		t.Fatalf("code not normalised: %q", c.Code)
	}
	if _, ok := repo.coupons[rid]["SAVE10"]; !ok {
		t.Fatal("coupon not persisted")
	}
	if got := countEvents(repo, app.EventCouponUpserted); got != 1 {
		t.Fatalf("want 1 coupon.upserted event, got %d", got)
	}
}

func TestUpsertCoupon_InvalidRejected(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	_, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{Code: "X", Type: "bogus", Value: 10})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if len(repo.coupons[rid]) != 0 {
		t.Fatal("invalid coupon must not persist")
	}
}

func TestUpsertCoupon_RollsBackEventOnPersistFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.failUpsert = true
	a := newApp(repo)
	_, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{Code: "X", Type: "flat", Value: 100, Active: true})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if countEvents(repo, app.EventCouponUpserted) != 0 {
		t.Fatal("event must not be committed when the tx fails")
	}
}

func TestListCoupons(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	for _, code := range []string{"a", "b", "c"} {
		if _, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{Code: code, Type: "flat", Value: 100, Active: true}); err != nil {
			t.Fatalf("seed %s: %v", code, err)
		}
	}
	got, err := a.ListCoupons(context.Background(), rid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 coupons, got %d", len(got))
	}
	// other restaurants are isolated.
	other, _ := a.ListCoupons(context.Background(), "out_OTHER")
	if len(other) != 0 {
		t.Fatalf("want 0 coupons for other outlet, got %d", len(other))
	}
}

func TestToggleCoupon(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	if _, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{Code: "save10", Type: "percent", Value: 10, Active: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// case-insensitive toggle off.
	got, err := a.ToggleCoupon(context.Background(), rid, "SaVe10", false)
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if got.Active {
		t.Fatal("expected inactive")
	}
	if repo.coupons[rid]["SAVE10"].Active {
		t.Fatal("not persisted")
	}

	// unknown code -> NotFound.
	if _, err := a.ToggleCoupon(context.Background(), rid, "nope", true); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestEvaluate(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	seed := func(in domain.NewCouponInput) {
		t.Helper()
		if _, err := a.UpsertCoupon(context.Background(), rid, in); err != nil {
			t.Fatalf("seed %s: %v", in.Code, err)
		}
	}
	seed(domain.NewCouponInput{Code: "pct10", Type: "percent", Value: 10, Active: true})                                  // 10000
	seed(domain.NewCouponInput{Code: "pct25", Type: "percent", Value: 25, Active: true})                                  // 25000
	seed(domain.NewCouponInput{Code: "flat50", Type: "flat", Value: 5000, Active: true})                                  // 5000
	seed(domain.NewCouponInput{Code: "min", Type: "flat", Value: 8000, Active: true, MinOrder: inr(200000)})              // blocked under 2000.00
	seed(domain.NewCouponInput{Code: "drinks", Type: "percent", Value: 40, Active: true, Category: "drinks"})             // drinks only
	seed(domain.NewCouponInput{Code: "off", Type: "percent", Value: 90, Active: false})                                  // inactive
	seed(domain.NewCouponInput{Code: "expired", Type: "percent", Value: 90, Active: true, EndsAt: "2026-06-01T00:00:00Z"}) // expired

	tests := []struct {
		name        string
		in          app.EvaluateInput
		wantApplied string
		wantMinor   int64
	}{
		{"best of all active", app.EvaluateInput{Subtotal: inr(100000)}, "PCT25", 25000},
		{"specific code", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "flat50"}, "FLAT50", 5000},
		{"min order gate excludes when under", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "min"}, "", 0},
		{"min order gate met", app.EvaluateInput{Subtotal: inr(200000), CouponCode: "min"}, "MIN", 8000},
		{"category restricted wins for drinks", app.EvaluateInput{Subtotal: inr(100000), Category: "drinks"}, "DRINKS", 40000},
		{"category restricted excluded for mains", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "drinks", Category: "mains"}, "", 0},
		{"inactive excluded", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "off"}, "", 0},
		{"expired excluded", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "expired"}, "", 0},
		{"unknown code yields nothing", app.EvaluateInput{Subtotal: inr(100000), CouponCode: "ghost"}, "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev, err := a.Evaluate(context.Background(), rid, tc.in)
			if err != nil {
				t.Fatalf("evaluate: %v", err)
			}
			if ev.Applied != tc.wantApplied {
				t.Fatalf("applied = %q, want %q", ev.Applied, tc.wantApplied)
			}
			if ev.Discount.Minor != tc.wantMinor {
				t.Fatalf("discount = %d, want %d", ev.Discount.Minor, tc.wantMinor)
			}
		})
	}
}

func TestEvaluate_EmitsPromoAppliedOnlyWhenDiscounted(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	if _, err := a.UpsertCoupon(context.Background(), rid, domain.NewCouponInput{Code: "pct10", Type: "percent", Value: 10, Active: true}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// discount granted -> one promo.applied event.
	if _, err := a.Evaluate(context.Background(), rid, app.EvaluateInput{Subtotal: inr(100000)}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countEvents(repo, app.EventPromoApplied); got != 1 {
		t.Fatalf("want 1 promo.applied event, got %d", got)
	}

	// no discount (unknown code) -> no additional promo.applied event.
	if _, err := a.Evaluate(context.Background(), rid, app.EvaluateInput{Subtotal: inr(100000), CouponCode: "ghost"}); err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if got := countEvents(repo, app.EventPromoApplied); got != 1 {
		t.Fatalf("promo.applied must not fire without a discount; got %d", got)
	}
}

func TestEvaluate_NegativeSubtotalRejected(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	_, err := a.Evaluate(context.Background(), rid, app.EvaluateInput{Subtotal: money.New(-1, "INR")})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func countEvents(repo *fakeRepo, typ string) int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	n := 0
	for _, e := range repo.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}
