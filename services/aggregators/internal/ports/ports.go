// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, connector-hub client, catalog client, ordering client, nats consumer)
// implement them; unit tests supply in-memory fakes. The app NEVER imports an
// adapter directly (CONVENTIONS.md dependency rule: adapters -> app -> domain).
package ports

import (
	"context"

	"github.com/restorna/platform/services/aggregators/internal/domain"
)

// Repository is the persistence port for the ExternalOrder aggregate. Every
// read/write is scoped to the restaurant via RLS (app.tenant_id), keyed by
// restaurant_id (the aggregators tenant key — orders are per-outlet).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events + processed-event mark commit atomically with the write.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// Get loads one external order by id (RLS-scoped). ErrNotFound if absent.
	Get(ctx context.Context, restaurantID, id string) (domain.ExternalOrder, error)

	// List returns external orders for the restaurant, optionally filtered by
	// connector id and/or status (empty string = no filter on that dimension).
	List(ctx context.Context, restaurantID, connectorID, status string) ([]domain.ExternalOrder, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent +
// MarkProcessed land in the same transaction (transactional outbox).
type Tx interface {
	// Get loads an order by id within this tx (RLS-scoped). ErrNotFound if absent.
	Get(ctx context.Context, id string) (domain.ExternalOrder, error)
	// GetByRef loads an order by (connector_id, external_ref) within this tx. Used
	// for idempotency on redelivered webhooks. ErrNotFound if absent.
	GetByRef(ctx context.Context, connectorID, externalRef string) (domain.ExternalOrder, error)
	// Insert persists a brand-new external order.
	Insert(ctx context.Context, o domain.ExternalOrder) error
	// Update persists a status change on an existing order.
	Update(ctx context.Context, o domain.ExternalOrder) error

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error

	// MarkProcessed records a consumed event id in this same tx (idempotent
	// choreography). No-op for an empty id.
	MarkProcessed(ctx context.Context, restaurantID, eventID string) error
	// Seen reports whether eventID was already processed for the restaurant,
	// within this tx (used to short-circuit a redelivered webhook).
	Seen(ctx context.Context, restaurantID, eventID string) (bool, error)
}

// MenuItem is one catalog item flattened for the aggregator menu push.
type MenuItem struct {
	ID          string
	CategoryID  string
	Name        string
	Description string
	PriceMinor  int64
	Currency    string
	Veg         bool
	Available   bool
	Station     string
}

// Catalog fetches the current menu for a restaurant. The catalog client adapter
// implements this against CatalogService.ListAllItems; unit tests use a fake.
type Catalog interface {
	// ListAllItems returns every menu item for the restaurant (incl. unavailable),
	// which PushMenu serializes and pushes to the aggregator.
	ListAllItems(ctx context.Context, restaurantID string) ([]MenuItem, error)
}

// ResolvedConnector is the connector-hub resolution result: which aggregator is
// active for this tenant plus its (decrypted) config.
type ResolvedConnector struct {
	ConnectorID    string
	InstallationID string
	TestMode       bool
	Config         map[string]string
}

// ConnectorHub resolves the active aggregator connector for a tenant and pushes a
// serialized menu to it. The client adapter implements this against
// ConnectorHubService.Resolve + the concrete AggregatorConnector adapter; unit
// tests use a fake.
type ConnectorHub interface {
	// Resolve picks the active aggregator connector for the restaurant. When
	// preferConnectorID is non-empty the hub is asked for that specific connector.
	Resolve(ctx context.Context, restaurantID, preferConnectorID string) (ResolvedConnector, error)
	// PushMenu serializes and pushes the menu to the resolved aggregator adapter,
	// returning the number of items the aggregator accepted.
	PushMenu(ctx context.Context, rc ResolvedConnector, menuJSON []byte) (int, error)
}

// OrderLine is one line forwarded to ordering.PlaceOrder.
type OrderLine struct {
	MenuItemID string
	Name       string
	Qty        int32
	PriceMinor int64
	Currency   string
}

// Ordering forwards an ingested external order into the OMS via
// OrderingService.PlaceOrder, so it hits the kitchen like any dine-in order. The
// client adapter implements this against the generated ordering client; unit
// tests use a fake.
type Ordering interface {
	// PlaceOrder places a dine-in order at the synthetic table for the restaurant
	// and returns the created order id.
	PlaceOrder(ctx context.Context, restaurantID, table string, lines []OrderLine) (orderID string, err error)
}
