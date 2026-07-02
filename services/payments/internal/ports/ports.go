// Package ports declares the interfaces the app layer depends on. Adapters (pg,
// connector-hub client, provider factory, nats consumer) implement them; unit
// tests supply in-memory fakes. The app NEVER imports an adapter directly
// (CONVENTIONS.md dependency rule: adapters -> app -> domain).
package ports

import (
	"context"
	"encoding/json"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/domain"
)

// Repository is the persistence port for the Payment aggregate. Implementations
// must scope every read/write to the restaurant via RLS (app.tenant_id).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS).
	// Staged outbox events + processed-event marks commit atomically with writes.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// Get loads a payment by id (RLS-scoped to the caller's restaurant).
	Get(ctx context.Context, restaurantID, paymentID string) (domain.Payment, error)

	// FindByIdempotencyKey returns the payment previously created for key within
	// the restaurant, or domain.ErrNotFound. Used to make CreateIntent idempotent.
	FindByIdempotencyKey(ctx context.Context, restaurantID, key string) (domain.Payment, error)

	// FindByProviderRef matches a webhook to its payment by the gateway ref. The
	// consumer scopes to the event's tenant; providerRef is globally unique per
	// provider so this is safe to look up cross-restaurant when tenant is unknown.
	FindByProviderRef(ctx context.Context, restaurantID, providerRef string) (domain.Payment, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent +
// MarkProcessed land in the same transaction (transactional outbox + idempotency).
type Tx interface {
	// Get loads a payment by id within this tx (RLS-scoped).
	Get(ctx context.Context, paymentID string) (domain.Payment, error)
	// Insert persists a brand-new payment plus its idempotency key.
	Insert(ctx context.Context, p domain.Payment, idempotencyKey string) error
	// Update persists status/method/refund changes to an existing payment.
	Update(ctx context.Context, p domain.Payment) error

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error

	// MarkProcessed records a consumed webhook event id in this same tx so a
	// redelivery is a no-op. A no-op for command RPCs that pass an empty id.
	MarkProcessed(ctx context.Context, restaurantID, eventID string) error
}

// ConnectorHub resolves the active payment provider + decrypted config for a
// tenant. The client adapter wraps ConnectorHubService.Resolve; tests use a fake.
type ConnectorHub interface {
	// ResolvePayment returns the connector id + config to instantiate for this
	// restaurant. preferConnectorID is an optional hint (falls back to the
	// tenant's default payment connector).
	ResolvePayment(ctx context.Context, restaurantID, preferConnectorID string) (Resolved, error)
}

// Resolved is the connector-hub resolution result for a capability.
type Resolved struct {
	ConnectorID    string
	InstallationID string
	TestMode       bool
	Config         map[string]string
}

// ProviderFactory instantiates a concrete payment provider adapter from a resolved
// connector id + config. The adapter wraps pkg/connectors.New; the app stays free
// of the connector SDK internals and drives the provider through this port.
type ProviderFactory interface {
	// Payment builds a payment provider bound to the resolved config.
	Payment(ctx context.Context, connectorID string, cfg map[string]string) (PaymentProvider, error)
}

// PaymentProvider is the app's view of a gateway adapter (mirrors
// pkg/connector.PaymentConnector without leaking the SDK types into app/domain).
type PaymentProvider interface {
	CreateIntent(ctx context.Context, amount money.Money, ref string) (providerRef string, err error)
	Capture(ctx context.Context, providerRef string) (receipt json.RawMessage, err error)
	Refund(ctx context.Context, providerRef string, amount money.Money) error
}
