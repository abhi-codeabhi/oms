// Package ports declares the interfaces the app layer depends on. Adapters (pg,
// connector-hub client, provider factory, nats consumer) implement them; unit tests
// supply in-memory fakes. The app NEVER imports an adapter directly.
package ports

import (
	"context"

	"github.com/restorna/platform/services/notifications/internal/domain"
)

// Repository is the persistence port for messages + templates. Implementations must
// scope every read/write to the owner via RLS (app.tenant_id).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to ownerID (RLS). Message
	// persistence + any staged outbox event commit atomically.
	Atomic(ctx context.Context, ownerID string, fn func(Tx) error) error

	// GetMessage returns a message by id (RLS-scoped to the caller's owner).
	GetMessage(ctx context.Context, ownerID, messageID string) (domain.Message, error)
	// FindByIdempotencyKey returns a prior message for (ownerID, key) if one exists,
	// so Send is idempotent. ok=false means no prior message.
	FindByIdempotencyKey(ctx context.Context, ownerID, key string) (domain.Message, bool, error)
	// FindByProviderRef locates a message by the provider id + reference (for
	// delivery-status webhooks). ok=false means no match.
	FindByProviderRef(ctx context.Context, providerID, providerRef string) (domain.Message, bool, error)

	// GetTemplate resolves the owner's template for id (falling back to the platform
	// default is the app's concern, not the repo's). Returns ErrNotFound if absent.
	GetTemplate(ctx context.Context, ownerID, templateID string) (domain.Template, error)
	// ListTemplates returns the owner's templates, sorted by id.
	ListTemplates(ctx context.Context, ownerID string) ([]domain.Template, error)
	// UpsertTemplate inserts or replaces a template (owner-scoped).
	UpsertTemplate(ctx context.Context, t domain.Template) error

	// UpdateDeliveryStatus persists a message's advanced status (delivery webhook).
	UpdateDeliveryStatus(ctx context.Context, m domain.Message) error

	// MarkEventProcessed records event id as handled and reports whether it was new
	// (false = already processed → the consumer skips it, giving exactly-once effect).
	MarkEventProcessed(ctx context.Context, eventID string) (isNew bool, err error)
}

// Tx is the unit-of-work handed to Atomic's callback. The message write + any staged
// outbox event land in the same transaction (transactional outbox).
type Tx interface {
	InsertMessage(ctx context.Context, m domain.Message) error
	UpdateMessage(ctx context.Context, m domain.Message) error
	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, ownerID string, data any) error
}

// Resolution is the provider connector-hub picked for a capability at this tenant.
type Resolution struct {
	ConnectorID    string            // e.g. "twilio" | "msg91" | "" when none installed
	InstallationID string
	TestMode       bool
	Config         map[string]string // decrypted per-tenant config
	Installed      bool              // false => no provider installed; caller falls back
}

// ConnectorHub is the port to ConnectorHubService.Resolve. The adapter wraps the
// generated Connect client; the app stays infra-free. Resolve returns the active
// notification provider for the tenant (or Installed=false when none is configured).
type ConnectorHub interface {
	Resolve(ctx context.Context, ownerID string) (Resolution, error)
}

// NotificationSender is the minimal outbound surface the app needs from a provider
// adapter (a subset of connector.NotificationConnector). Send delivers a message
// over channel and returns the provider's message reference.
type NotificationSender interface {
	Send(ctx context.Context, channel, to, subject, body string) (providerRef string, err error)
}

// ProviderFactory instantiates a NotificationSender from a resolved connector id +
// config. The adapter delegates to pkg/connectors.NewNotification; when the id is
// unknown or config invalid it returns an error and the app falls back to the mock.
type ProviderFactory interface {
	// New builds the provider for connectorID with cfg. Fallback returns the
	// built-in lognotify mock so OTP/invite flows still work without a real provider.
	New(ctx context.Context, connectorID string, cfg map[string]string) (NotificationSender, error)
	// Fallback returns the built-in mock sender (lognotify) and its connector id.
	Fallback(ctx context.Context) (NotificationSender, string)
}
