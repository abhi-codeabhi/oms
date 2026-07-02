// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, catalog client, nats consumer) implement them; unit tests supply in-memory
// fakes. The app NEVER imports an adapter directly (CONVENTIONS.md dependency
// rule: adapters -> app -> domain).
package ports

import (
	"context"

	"github.com/restorna/platform/services/kitchen/internal/domain"
)

// Repository is the persistence port for the Ticket aggregate. Implementations
// must scope every read/write to the restaurant via RLS (app.tenant_id), keyed by
// restaurant_id (the kitchen's tenant key — KDS is per-outlet).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events are committed atomically with the business writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// List returns all live tickets for the restaurant (board/queue/all-day derive
	// their views in the domain from this set).
	List(ctx context.Context, restaurantID string) ([]domain.Ticket, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	// Get loads a ticket by id within this tx (RLS-scoped). Returns
	// domain.ErrNotFound if it does not exist for the restaurant.
	Get(ctx context.Context, ticketID string) (domain.Ticket, error)
	// Insert persists a brand-new ticket.
	Insert(ctx context.Context, t domain.Ticket) error
	// Update persists changes to an existing ticket (items/served).
	Update(ctx context.Context, t domain.Ticket) error

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error

	// MarkProcessed records a consumed event id in this same tx (idempotent
	// choreography). Committed atomically with the ticket insert so a redelivery
	// is a no-op. A no-op for command RPCs that pass an empty id.
	MarkProcessed(ctx context.Context, restaurantID, eventID string) error
}

// MenuResolver resolves a menu item to its display name + kitchen station. The
// catalog client adapter implements this against CatalogService.GetItem; unit
// tests use a fake. Used by the OrderPlaced consumer to enrich lines that lack a
// resolved name/station before a ticket is created.
type MenuResolver interface {
	Resolve(ctx context.Context, restaurantID, itemID string) (ResolvedItem, error)
}

// ResolvedItem is the catalog lookup result for a menu item.
type ResolvedItem struct {
	Name    string
	Station string
}

// ProcessedEvents records consumed event ids for idempotent choreography. The
// OrderPlaced consumer marks an event id processed in the same tx as the ticket
// insert so a redelivery is a no-op (exactly-once effect).
type ProcessedEvents interface {
	// Seen reports whether eventID was already processed for the restaurant.
	Seen(ctx context.Context, restaurantID, eventID string) (bool, error)
}
