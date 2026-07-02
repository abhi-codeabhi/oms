// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, ordering/catalog/settings/promotions clients, nats consumers) implement
// them; unit tests supply in-memory fakes. The app NEVER imports an adapter
// directly (CONVENTIONS.md dependency rule: adapters -> app -> domain).
package ports

import (
	"context"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/domain"
)

// Repository is the persistence port for the Bill aggregate AND the event-driven
// Tab read model. Every read/write is scoped to the restaurant via RLS
// (app.tenant_id), keyed by restaurant_id (billing is per-outlet).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS). The
	// staged outbox events + processed-event marks commit atomically with writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// GetBill loads one bill by id (RLS-scoped). ErrNotFound if absent.
	GetBill(ctx context.Context, restaurantID, billID string) (domain.Bill, error)
	// ListOpenBills returns the unpaid bills for the restaurant (the billing queue).
	ListOpenBills(ctx context.Context, restaurantID string) ([]domain.Bill, error)
	// ListTabs returns the live billing board for the restaurant (read model).
	ListTabs(ctx context.Context, restaurantID string) ([]domain.Tab, error)
	// Seen reports whether eventID was already processed (consumer short-circuit).
	Seen(ctx context.Context, restaurantID, eventID string) (bool, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent +
// MarkProcessed land in the same transaction (transactional outbox + dedupe).
type Tx interface {
	// GetBill loads a bill by id within this tx (RLS-scoped).
	GetBill(ctx context.Context, billID string) (domain.Bill, error)
	// InsertBill persists a brand-new bill.
	InsertBill(ctx context.Context, b domain.Bill) error
	// UpdateBill persists changes to an existing bill (discount/payments/paid).
	UpdateBill(ctx context.Context, b domain.Bill) error

	// GetTab loads the tab for a numeric table (zero-value + found=false if absent).
	GetTab(ctx context.Context, table int32) (domain.Tab, bool, error)
	// UpsertTab inserts or replaces the tab row for its table.
	UpsertTab(ctx context.Context, t domain.Tab) error
	// DeleteTab removes the tab for a numeric table (on bill finalized).
	DeleteTab(ctx context.Context, table int32) error

	// Seen reports whether eventID was already processed for the restaurant,
	// within this tx (so the dedupe check + the write commit atomically).
	Seen(ctx context.Context, restaurantID, eventID string) (bool, error)
	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error
	// MarkProcessed records a consumed event id in this same tx (idempotent
	// choreography). A no-op for an empty id.
	MarkProcessed(ctx context.Context, restaurantID, eventID string) error
}

// --- outbound service clients (the app calls OTHER services) ---

// Orders is the port to OrderingService: list a table's unbilled orders and mark
// them billed once the aggregated bill is opened. The grpc client adapter
// implements it; tests use a fake.
type Orders interface {
	// ListForTable returns the table's UNBILLED orders (include_billed=false).
	ListForTable(ctx context.Context, restaurantID, table string) ([]Order, error)
	// MarkBilled flags the given orders billed so they can't be billed twice.
	MarkBilled(ctx context.Context, restaurantID string, orderIDs []string) error
}

// Order is the slimmed OrderingService order the billing app consumes.
type Order struct {
	ID    string
	Table string
	Lines []OrderLine
}

// OrderLine is one line of an order (menu item + qty + per-unit price + any name
// already resolved by ordering).
type OrderLine struct {
	MenuItemID string
	Name       string
	Qty        int32
	UnitPrice  money.Money
}

// Menu is the port to CatalogService: resolve a menu item's display name +
// course category before aggregating bill lines.
type Menu interface {
	// GetItem resolves a menu item's name + category (ErrNotFound if unknown).
	GetItem(ctx context.Context, restaurantID, itemID string) (ResolvedItem, error)
}

// ResolvedItem is the catalog lookup result for a menu item.
type ResolvedItem struct {
	Name     string
	Category string
}

// Settings is the port to SettingsService: read the effective billing tax config
// for a restaurant (billing.gst_pct, billing.service_charge_pct, billing.rounding,
// billing.currency) on the billing hot path.
type Settings interface {
	// BillingConfig returns the effective TaxConfig for the restaurant.
	BillingConfig(ctx context.Context, restaurantID string) (domain.TaxConfig, error)
}

// Promotions is the port to PromotionsService: evaluate a coupon for a subtotal
// to produce a discount amount (used by ApplyDiscount when a coupon is supplied).
type Promotions interface {
	// Evaluate returns the discount minor units for a coupon code against a
	// subtotal (0 when the coupon does not apply). applied describes what matched.
	Evaluate(ctx context.Context, restaurantID, couponCode string, subtotal money.Money) (discountMinor int64, applied string, err error)
}
