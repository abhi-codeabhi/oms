// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, entitlements client, crypto, nats publisher, connector registry) implement
// them; unit tests supply in-memory fakes. The app NEVER imports an adapter
// directly (CONVENTIONS.md: adapters -> app -> domain).
package ports

import (
	"context"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
)

// Repository is the persistence port for the Installation aggregate. Every
// read/write is scoped to the owner via RLS (app.tenant_id set per tx).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to ownerID (RLS). Staged
	// outbox events commit atomically with the business writes.
	Atomic(ctx context.Context, ownerID string, fn func(Tx) error) error

	GetInstallation(ctx context.Context, ownerID, installationID string) (domain.Installation, error)
	ListInstallations(ctx context.Context, ownerID string) ([]domain.Installation, error)
	// ActiveByConnector returns installations for a connector id under the owner
	// (used by Resolve). Only enabled ones are relevant; caller filters.
	ListByOwner(ctx context.Context, ownerID string) ([]domain.Installation, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	InsertInstallation(ctx context.Context, i domain.Installation) error
	UpdateInstallation(ctx context.Context, i domain.Installation) error
	GetInstallation(ctx context.Context, installationID string) (domain.Installation, error)
	// ExistsForConnector reports whether the owner already installed connectorID at
	// the same restaurant scope (idempotency / uniqueness for quota correctness).
	ExistsForConnector(ctx context.Context, ownerID, restaurantID, connectorID string) (bool, error)

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, ownerID string, data any) error
}

// Entitlements is the port to the EntitlementsService. The app reserves quota
// before installing a connector and checks the capability feature flag before
// exposing a connector in the marketplace.
type Entitlements interface {
	// ReserveQuota atomically reserves `delta` of `key` for ownerID, idempotent by
	// reservationID. Returns ok=false (with an upgrade hint) when over limit.
	ReserveQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) (ReserveResult, error)
	// ReleaseQuota returns reserved quota (compensation on a failed install).
	ReleaseQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) error
	// HasFeature reports whether a boolean feature flag is enabled for the owner.
	HasFeature(ctx context.Context, ownerID, feature string) (bool, error)
}

// ReserveResult is the outcome of a quota reservation.
type ReserveResult struct {
	OK          bool
	Remaining   int64
	UpgradeHint string
}

// Crypto is the envelope-encryption port. Implemented by an AES-256-GCM adapter
// backed by a KEK from the environment (CONNECTOR_KEK). The app encrypts secret
// config before persisting and decrypts on Resolve; plaintext never persists.
type Crypto interface {
	EncryptMap(m map[string]string) ([]byte, error)
	DecryptMap(blob []byte) (map[string]string, error)
}

// EventBus publishes normalized CloudEvents produced from inbound webhooks to
// NATS. IngestWebhook publishes directly (the webhook is not part of a DB tx);
// outbox-staged events go through the relay instead.
type EventBus interface {
	Publish(ctx context.Context, e events.Event) error
}

// ConnectorManifest is the app-facing view of a connector's manifest (mirrors
// pkg/connector.Manifest, decoupled so the app owns its shape). It carries the
// per-key secret spec derived from the config schema.
type ConnectorManifest struct {
	ID           string
	Name         string
	Capabilities []domain.Capability
	ConfigSchema []byte // raw JSON schema (keys + secret flags)
	LogoURL      string
	// SecretKeys marks which config keys are secret (encrypted + write-only).
	SecretKeys map[string]bool
}

// ConfigSpec builds the domain split-spec from the manifest.
func (m ConnectorManifest) ConfigSpec() domain.ConfigSpec {
	sec := map[string]bool{}
	for k, v := range m.SecretKeys {
		if v {
			sec[k] = true
		}
	}
	return domain.ConfigSpec{Secret: sec}
}

// Webhook is the normalized result of a connector authenticating + parsing an
// inbound provider webhook. The connector verifies the signature (rejecting
// tampered payloads) and returns a CloudEvent to publish.
type Webhook struct {
	Event    events.Event
	Verified bool
}

// Connectors is the registry port over pkg/connectors: it lists connector
// manifests and verifies inbound webhooks by delegating to the adapter for a
// connector id. Implemented by adapters/registry, which wraps connectors.All()
// and connectors.New(id, cfg).
type Connectors interface {
	// All returns the manifests of every registered connector (unfiltered).
	All() []ConnectorManifest
	// Get returns a single connector's manifest by id.
	Get(id string) (ConnectorManifest, bool)
	// VerifyWebhook builds the connector, hands it the raw body + headers, and
	// returns the normalized event. verified=false / error => reject the webhook.
	// cfg is the decrypted config for the tenant's installation (credentials the
	// connector needs to check the signature).
	VerifyWebhook(ctx context.Context, connectorID string, cfg map[string]string, body []byte, headers map[string]string) (Webhook, error)
}
