// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, the kitchen/billing/settings/ordering clients, the nats consumers)
// implement them; unit tests supply in-memory fakes. The app NEVER imports an
// adapter directly (CONVENTIONS.md dependency rule: adapters -> app -> domain).
package ports

import (
	"context"

	"github.com/restorna/platform/services/floor/internal/domain"
)

// Repository is the persistence port for the single Floor aggregate per
// restaurant. Implementations scope every read/write to the restaurant via RLS
// (app.tenant_id = restaurant_id — the floor's tenant key is per-outlet).
type Repository interface {
	// Get loads the floor doc for the restaurant, or domain.ErrNotFound if the
	// floor was never initialised.
	Get(ctx context.Context, restaurantID string) (domain.Floor, error)

	// Save upserts the whole floor doc (the floor is read/written wholesale; the
	// table set is small).
	Save(ctx context.Context, restaurantID string, f domain.Floor) error

	// Atomic runs fn in a single tenant-scoped transaction. The floor upsert, any
	// staged outbox events, and the processed-event mark commit together — so an
	// event redelivery is a no-op (idempotent choreography).
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error
}

// Tx is the unit-of-work handed to Atomic's callback (transactional outbox).
type Tx interface {
	// Get loads the floor doc within this tx (RLS-scoped); domain.ErrNotFound if
	// absent (callers create it on first use).
	Get(ctx context.Context, restaurantID string) (domain.Floor, error)
	// Save upserts the floor doc within this tx.
	Save(ctx context.Context, restaurantID string, f domain.Floor) error
	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error
	// MarkProcessed records a consumed event id in this same tx (idempotent
	// choreography). A no-op for command RPCs that pass an empty id.
	MarkProcessed(ctx context.Context, restaurantID, eventID string) error
	// Seen reports whether eventID was already processed for the restaurant
	// (consumer short-circuit before doing work).
	Seen(ctx context.Context, restaurantID, eventID string) (bool, error)
}

// KitchenBoard reads the live kitchen work for a restaurant. Implemented by the
// KitchenService client (GetBoard = cooking tickets, ServeQueue = ready,
// unserved). The app groups these onto floor tables to DERIVE per-table status.
type KitchenBoard interface {
	// Board returns the still-cooking tickets (KitchenService.GetBoard).
	Board(ctx context.Context, restaurantID string) ([]KitchenTicket, error)
	// ServeQueue returns the ready-but-unserved tickets (KitchenService.ServeQueue).
	ServeQueue(ctx context.Context, restaurantID string) ([]KitchenTicket, error)
}

// KitchenTicket is the slice of a kitchen ticket the floor cares about: which
// table it belongs to (so it can be tallied as cooking/ready load).
type KitchenTicket struct {
	Table string
}

// BillingOpen reads the open (unpaid) bills for a restaurant. Implemented by the
// BillingService client (ListOpen). A table with an open bill derives to billing.
type BillingOpen interface {
	// ListOpen returns the open bills' table labels (BillingService.ListOpen).
	ListOpen(ctx context.Context, restaurantID string) ([]OpenBill, error)
}

// OpenBill is the slice of an open bill the floor needs: its table label.
type OpenBill struct {
	Table string
}

// SettingsResolver reads the effective nudge config for a restaurant from
// SettingsService.GetEffective (floor.nudge.* keys). Implemented by the settings
// client; the app falls back to domain defaults when a key is missing.
type SettingsResolver interface {
	// NudgeConfig resolves floor.nudge.* for the restaurant (override -> default).
	NudgeConfig(ctx context.Context, restaurantID string) (domain.NudgeConfig, error)
}

// OrderRelocator moves open orders between table labels. Implemented by the
// OrderingService client (Relocate). Called on Move/Swap so the orders follow the
// seat. (Kitchen ticket relocation needs a future kitchen.Relocate RPC — see README.)
type OrderRelocator interface {
	// Relocate moves all open orders from one table label to another and returns
	// the count moved (OrderingService.Relocate).
	Relocate(ctx context.Context, restaurantID, fromTable, toTable string) (int, error)
}
