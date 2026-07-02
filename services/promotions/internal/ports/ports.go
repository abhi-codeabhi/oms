// Package ports declares the interfaces the app layer depends on. Adapters (pg,
// event publisher) implement them; unit tests supply in-memory fakes. The app
// NEVER imports an adapter directly.
package ports

import (
	"context"

	"github.com/restorna/platform/services/promotions/internal/domain"
)

// Repository is the persistence port for the promotions aggregate. Implementations
// must scope every read/write to the restaurant (outlet) via RLS (app.tenant_id),
// which is the restaurant_id from the auth context. Coupons are keyed by Code within
// a restaurant.
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events commit atomically with the business writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// ListCoupons returns every coupon for the restaurant (own tenant-scoped tx).
	ListCoupons(ctx context.Context, restaurantID string) ([]domain.Coupon, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	// UpsertCoupon creates or replaces a coupon keyed by (restaurant_id, code).
	UpsertCoupon(ctx context.Context, c domain.Coupon) error
	// GetCoupon loads a coupon by code within the tx's restaurant (ErrNotFound if absent).
	GetCoupon(ctx context.Context, code string) (domain.Coupon, error)

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, tenantID string, data any) error
}
