// Package ports declares the interfaces the app layer depends on. Adapters
// (Postgres, in-memory fakes for tests) implement them; the app never imports an
// adapter directly. Dependency rule: app -> ports -> domain.
package ports

import (
	"context"

	"github.com/restorna/platform/services/settings/internal/domain"
)

// DefinitionRepo stores and retrieves setting definitions. Definitions are GLOBAL
// (not tenant-scoped): every owner sees the same catalog. RegisterDefinitions does
// an idempotent upsert keyed by Definition.Key.
type DefinitionRepo interface {
	// UpsertDefinitions idempotently inserts/updates definitions by key. Returns
	// the number written.
	UpsertDefinitions(ctx context.Context, defs []domain.Definition) (int, error)
	// GetDefinition loads one definition by key; domain.ErrNotFound if absent.
	GetDefinition(ctx context.Context, key string) (domain.Definition, error)
	// ListDefinitions returns all definitions in a namespace ("" = all).
	ListDefinitions(ctx context.Context, namespace string) ([]domain.Definition, error)
}

// OverrideRepo stores per-tenant override values. Overrides are owner-scoped (RLS
// by owner_id). SetOverride upserts the override AND stages the change event in one
// transaction so persistence + outbox commit atomically.
type OverrideRepo interface {
	// SetOverride upserts an override at its scope and, in the SAME transaction,
	// stages an outbox event of type evt.Type with payload evt.Data for tenant
	// evt.TenantID. The override row is identified by (key, owner, brand,
	// restaurant, scope).
	SetOverride(ctx context.Context, o domain.Override, evt OverrideEvent) error
	// OverridesFor returns every override for the given keys that could apply to
	// the (owner, brand, restaurant) tuple — i.e. owner-level rows, plus brand-level
	// rows for brandID, plus restaurant-level rows for restaurantID. Resolution
	// (precedence) happens in the domain. An empty keys slice means "all keys".
	OverridesFor(ctx context.Context, ownerID, brandID, restaurantID string, keys []string) ([]domain.Override, error)
}

// OverrideEvent is the change event the app asks the repo to stage transactionally
// alongside the override write.
type OverrideEvent struct {
	Type     string
	TenantID string
	Data     any
}

// Cache is the in-process effective-value cache on the GetEffective hot path. It
// is keyed by (owner, brand, restaurant, key) and invalidated on SetOverride. A
// nil Cache disables caching (the app guards for it).
type Cache interface {
	// Get returns the cached value and true on a live (un-expired) hit.
	Get(key string) (domain.SettingValue, bool)
	// Set stores a value with the cache's configured TTL.
	Set(key string, v domain.SettingValue)
	// InvalidateOwner drops every entry belonging to an owner (called after a
	// SetOverride changes that owner's effective values).
	InvalidateOwner(ownerID string)
}
