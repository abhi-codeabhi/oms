// Package ports declares the interfaces the app layer depends on. Adapters (pg,
// event publisher) implement them; unit tests supply in-memory fakes. The app
// NEVER imports an adapter directly.
package ports

import (
	"context"

	"github.com/restorna/platform/services/ordering/internal/domain"
)

// Repository is the persistence port for the order aggregate. Implementations
// must scope every read/write to the restaurant via RLS (app.tenant_id).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events are committed atomically with the business writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// GetOrder returns one order by id (RLS-scoped to the caller's restaurant).
	GetOrder(ctx context.Context, restaurantID, orderID string) (domain.Order, error)
	// ListForRestaurant returns all orders for the restaurant, newest first. The
	// app filters tolerantly by table label and billed flag in memory (the proven
	// Node listForTable rule) so DB-side normalisation isn't required.
	ListForRestaurant(ctx context.Context, restaurantID string) ([]domain.Order, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	InsertOrder(ctx context.Context, o domain.Order) error
	// SetBilled flips the billed flag for one order; returns ErrNotFound if absent.
	SetBilled(ctx context.Context, orderID string, billed bool) error
	// SetTable updates an order's table label (relocate).
	SetTable(ctx context.Context, orderID, tableID string) error
	// GetOrder reads one order inside this tx (for read-modify-write).
	GetOrder(ctx context.Context, orderID string) (domain.Order, error)
	// ListForRestaurant reads all orders for the tx's restaurant.
	ListForRestaurant(ctx context.Context, restaurantID string) ([]domain.Order, error)

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType string, tenantID string, data any) error
}
