// Package domain holds the pure promotions model: the Coupon aggregate plus the
// discount Evaluate engine. It imports NO infrastructure (no pgx, nats, connect).
// Rules live here; adapters map this to/from proto and SQL. (Ported from the proven
// Restorna Node promotions service: domain/coupon.js + domain/engine.js.)
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/money"
)

// Coupon types (mirrors the Node demo: 'percent' | 'flat').
const (
	TypePercent = "percent" // value is a 0..100 percentage of the subtotal
	TypeFlat    = "flat"    // value is a flat amount in minor units
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// Coupon is a discount the customer can apply to a cart. Keyed by Code within a
// restaurant (the tenant). A coupon discounts either a percentage of the subtotal
// (TypePercent) or a flat amount (TypeFlat), gated by an optional minimum order, an
// optional category restriction, an optional active time window, and the active flag.
type Coupon struct {
	Code     string
	Type     string      // percent | flat
	Value    int64       // percent (0-100) or flat minor units
	MinOrder money.Money // minimum subtotal before the coupon applies (0 = none)
	Category string      // optional category restriction ("" = any category)
	Active   bool
	StartsAt *time.Time // optional window start (nil = no lower bound)
	EndsAt   *time.Time // optional window end (nil = no expiry)
}

// NewCouponInput is the validated construction input for a coupon (upsert).
type NewCouponInput struct {
	Code     string
	Type     string
	Value    int64
	MinOrder money.Money
	Category string
	Active   bool
	StartsAt string // RFC3339, optional
	EndsAt   string // RFC3339, optional
}

// NewCoupon validates and constructs a coupon. Code is required and normalised to
// upper-case (coupons are case-insensitive). Type must be percent|flat and value
// must be > 0; a percent value must be 1..100. The optional starts_at/ends_at are
// parsed as RFC3339 and, when both present, starts_at must not be after ends_at.
func NewCoupon(in NewCouponInput) (Coupon, error) {
	code := strings.ToUpper(strings.TrimSpace(in.Code))
	if code == "" {
		return Coupon{}, fieldErr("code is required")
	}
	typ := strings.ToLower(strings.TrimSpace(in.Type))
	if typ != TypePercent && typ != TypeFlat {
		return Coupon{}, fieldErr("type must be 'percent' or 'flat'")
	}
	if in.Value <= 0 {
		return Coupon{}, fieldErr("value must be > 0")
	}
	if typ == TypePercent && in.Value > 100 {
		return Coupon{}, fieldErr("percent value must be between 1 and 100")
	}
	if in.MinOrder.Minor < 0 {
		return Coupon{}, fieldErr("min_order must not be negative")
	}
	if in.MinOrder.Currency == "" {
		in.MinOrder.Currency = "INR"
	}

	starts, err := parseTime(in.StartsAt, "starts_at")
	if err != nil {
		return Coupon{}, err
	}
	ends, err := parseTime(in.EndsAt, "ends_at")
	if err != nil {
		return Coupon{}, err
	}
	if starts != nil && ends != nil && starts.After(*ends) {
		return Coupon{}, fieldErr("starts_at must not be after ends_at")
	}

	return Coupon{
		Code:     code,
		Type:     typ,
		Value:    in.Value,
		MinOrder: in.MinOrder,
		Category: strings.TrimSpace(in.Category),
		Active:   in.Active,
		StartsAt: starts,
		EndsAt:   ends,
	}, nil
}

// SetActive flips the coupon's active flag (toggle).
func (c *Coupon) SetActive(active bool) { c.Active = active }

// NormalizeCode trims and upper-cases a coupon code (coupons are case-insensitive
// and stored upper-cased). Lookups must normalise the same way.
func NormalizeCode(code string) string { return strings.ToUpper(strings.TrimSpace(code)) }

// ApplyResult is the outcome of evaluating a single coupon against a cart.
// Discount is 0 (and OK false) when the coupon does not apply. Reason explains why.
type ApplyResult struct {
	OK       bool
	Discount money.Money
	Reason   string
}

// Apply evaluates one coupon against a subtotal/category at a point in time. It is
// the faithful Go port of the Node demo's applyCoupon, extended with the proto's
// active time window (starts_at/ends_at) and category restriction:
//
//   - inactive coupons never apply;
//   - outside the [starts_at, ends_at] window the coupon never applies;
//   - a category-restricted coupon only applies when the cart category matches;
//   - the subtotal must meet min_order;
//   - percent => round(subtotal * value / 100), capped at the subtotal;
//   - flat    => min(value, subtotal) — never discount more than the subtotal.
//
// The returned discount currency always matches the subtotal currency.
func (c Coupon) Apply(subtotal money.Money, category string, now time.Time) ApplyResult {
	zero := money.New(0, subtotal.Currency)

	if !c.Active {
		return ApplyResult{OK: false, Discount: zero, Reason: "inactive"}
	}
	if c.StartsAt != nil && now.Before(*c.StartsAt) {
		return ApplyResult{OK: false, Discount: zero, Reason: "not_started"}
	}
	if c.EndsAt != nil && now.After(*c.EndsAt) {
		return ApplyResult{OK: false, Discount: zero, Reason: "expired"}
	}
	if c.Category != "" && c.Category != strings.TrimSpace(category) {
		return ApplyResult{OK: false, Discount: zero, Reason: "category_mismatch"}
	}
	if subtotal.Minor < c.MinOrder.Minor {
		return ApplyResult{OK: false, Discount: zero, Reason: "min_order_not_met"}
	}

	var discountMinor int64
	if c.Type == TypePercent {
		discountMinor = subtotal.Pct(float64(c.Value)).Minor
	} else {
		discountMinor = c.Value
	}
	// Never discount more than the subtotal (caps both percent and flat).
	if discountMinor > subtotal.Minor {
		discountMinor = subtotal.Minor
	}
	if discountMinor <= 0 {
		return ApplyResult{OK: false, Discount: zero, Reason: "no_discount"}
	}
	return ApplyResult{OK: true, Discount: money.New(discountMinor, subtotal.Currency)}
}

// Evaluation is the result of the discount engine: the best discount found and the
// code of the coupon that produced it ("" when nothing applied).
type Evaluation struct {
	Discount money.Money
	Applied  string
}

// Evaluate is the discount engine, ported from the Node demo's engine.js. It scans
// the coupon catalogue for the best (largest) applicable discount for the cart
// context and returns it together with the winning coupon's code.
//
// When couponCode is supplied, only that coupon is considered (the customer entered
// a specific code). When it is empty, every active coupon is considered and the
// best-of is chosen — a single best promotion wins (non-stacking), matching the Node
// engine's "take the larger discount" policy.
func Evaluate(coupons []Coupon, subtotal money.Money, couponCode, category string, now time.Time) Evaluation {
	out := Evaluation{Discount: money.New(0, subtotal.Currency), Applied: ""}
	want := strings.ToUpper(strings.TrimSpace(couponCode))

	for _, c := range coupons {
		if want != "" && c.Code != want {
			continue
		}
		res := c.Apply(subtotal, category, now)
		if !res.OK {
			continue
		}
		if res.Discount.Minor > out.Discount.Minor {
			out.Discount = res.Discount
			out.Applied = c.Code
		}
	}
	return out
}

// --- helpers ---

// parseTime parses an optional RFC3339 timestamp; "" yields nil (no bound).
func parseTime(s, field string) (*time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fieldErr(field + " must be an RFC3339 timestamp")
	}
	t = t.UTC()
	return &t, nil
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
