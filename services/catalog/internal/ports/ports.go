// Package ports declares the interfaces the app layer depends on. Adapters (pg,
// event publisher) implement them; unit tests supply in-memory fakes. The app
// NEVER imports an adapter directly.
package ports

import (
	"context"

	"github.com/restorna/platform/services/catalog/internal/domain"
)

// Repository is the persistence port for the catalog aggregate. Implementations
// must scope every read/write to the restaurant (outlet) via RLS (app.tenant_id),
// which is the restaurant_id from the auth context.
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events commit atomically with the business writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// Reads (each opens its own tenant-scoped tx).
	ListCategories(ctx context.Context, restaurantID string) ([]domain.Category, error)
	GetItem(ctx context.Context, restaurantID, itemID string) (domain.Item, error)
	// ListItems returns brand items with their per-outlet override (if any) already
	// applied to the effective price/availability.
	ListItems(ctx context.Context, restaurantID string) ([]domain.Item, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	UpsertCategory(ctx context.Context, c domain.Category) error
	UpsertItem(ctx context.Context, it domain.Item) error
	GetItem(ctx context.Context, itemID string) (domain.Item, error)

	// GetOverride loads the per-outlet override for an item (ok=false if none).
	GetOverride(ctx context.Context, itemID string) (domain.OutletOverride, bool, error)
	// PutOverride upserts a per-outlet price/availability override.
	PutOverride(ctx context.Context, ov domain.OutletOverride) error
	// ClearOverride removes the per-outlet override (revert to brand defaults).
	ClearOverride(ctx context.Context, itemID string) error

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, tenantID string, data any) error
}
