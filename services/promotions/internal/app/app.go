// Package app holds the promotions use cases. It depends only on ports + domain. It
// orchestrates persistence (repo) and event emission (outbox via Tx.StageEvent),
// and runs the pure discount engine (domain.Evaluate) for the Evaluate RPC. The grpc
// adapter maps proto <-> these calls; tests drive it with in-memory fakes.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/promotions/internal/domain"
	"github.com/restorna/platform/services/promotions/internal/ports"
)

// Event types emitted by this service (see CONVENTIONS.md naming).
const (
	EventCouponUpserted = "restorna.promotions.coupon.upserted.v1"
	EventPromoApplied   = "restorna.promotions.promo.applied.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps + windows.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo ports.Repository
	now  Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, now: now}
}

// UpsertCoupon validates and persists a coupon (create or replace, keyed by code
// within the restaurant), staging a coupon.upserted event in the same transaction.
func (a *App) UpsertCoupon(ctx context.Context, restaurantID string, in domain.NewCouponInput) (domain.Coupon, error) {
	c, err := domain.NewCoupon(in)
	if err != nil {
		return domain.Coupon{}, err
	}
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if err := tx.UpsertCoupon(ctx, c); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventCouponUpserted, restaurantID, couponEvent(restaurantID, c))
	})
	if err != nil {
		return domain.Coupon{}, err
	}
	return c, nil
}

// ListCoupons returns every coupon configured for the restaurant.
func (a *App) ListCoupons(ctx context.Context, restaurantID string) ([]domain.Coupon, error) {
	return a.repo.ListCoupons(ctx, restaurantID)
}

// ToggleCoupon flips a coupon active/inactive by code. NotFound if it does not exist.
func (a *App) ToggleCoupon(ctx context.Context, restaurantID, code string, active bool) (domain.Coupon, error) {
	var out domain.Coupon
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		c, err := tx.GetCoupon(ctx, normalizeCode(code))
		if err != nil {
			return err
		}
		c.SetActive(active)
		if err := tx.UpsertCoupon(ctx, c); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		return domain.Coupon{}, err
	}
	return out, nil
}

// EvaluateInput is the validated cart context for a discount evaluation.
type EvaluateInput struct {
	Subtotal   money.Money
	CouponCode string
	Category   string
}

// Evaluate runs the discount engine over the restaurant's coupon catalogue and
// returns the best discount plus the code of the coupon that applied (empty when
// none did). It validates active + time window + min_order + category match and caps
// percent/flat discounts at the subtotal (all in domain.Evaluate). When a discount is
// actually granted it stages a promo.applied event.
func (a *App) Evaluate(ctx context.Context, restaurantID string, in EvaluateInput) (domain.Evaluation, error) {
	if in.Subtotal.Minor < 0 {
		return domain.Evaluation{}, fmt.Errorf("%w: subtotal must not be negative", domain.ErrInvalid)
	}
	if in.Subtotal.Currency == "" {
		in.Subtotal.Currency = "INR"
	}

	coupons, err := a.repo.ListCoupons(ctx, restaurantID)
	if err != nil {
		return domain.Evaluation{}, err
	}

	result := domain.Evaluate(coupons, in.Subtotal, in.CouponCode, in.Category, a.now().UTC())

	if result.Discount.Minor > 0 {
		// promo.applied is best-effort telemetry; a staging failure must not deny the
		// customer the discount they earned, so we ignore the error here.
		_ = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
			return tx.StageEvent(ctx, EventPromoApplied, restaurantID, appliedEvent(restaurantID, in, result))
		})
	}
	return result, nil
}

// --- helpers ---

func normalizeCode(code string) string { return domain.NormalizeCode(code) }

// event payloads (kept small + stable; consumers project these).

func couponEvent(restaurantID string, c domain.Coupon) map[string]any {
	return map[string]any{
		"restaurant_id": restaurantID,
		"code":          c.Code,
		"type":          c.Type,
		"value":         c.Value,
		"min_order":     c.MinOrder.Minor,
		"currency":      c.MinOrder.Currency,
		"category":      c.Category,
		"active":        c.Active,
	}
}

func appliedEvent(restaurantID string, in EvaluateInput, ev domain.Evaluation) map[string]any {
	return map[string]any{
		"restaurant_id":  restaurantID,
		"subtotal_minor": in.Subtotal.Minor,
		"discount_minor": ev.Discount.Minor,
		"currency":       in.Subtotal.Currency,
		"applied":        ev.Applied,
		"category":       in.Category,
	}
}
