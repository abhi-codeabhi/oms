// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, entitlements client, blob store, event publisher) implement them; unit
// tests supply in-memory fakes. The app NEVER imports an adapter directly.
package ports

import (
	"context"

	"github.com/restorna/platform/services/tenant/internal/domain"
)

// Repository is the persistence port for the tenant aggregate. Implementations
// must scope every read/write to the owner via RLS (app.tenant_id).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to ownerID (RLS). The
	// staged outbox events are committed atomically with the business writes.
	Atomic(ctx context.Context, ownerID string, fn func(Tx) error) error

	GetOwner(ctx context.Context, ownerID string) (domain.Owner, error)
	// ListOwners returns a cross-tenant, paginated index of owners (platform-admin
	// only). query optionally filters by name (case-insensitive substring). This
	// bypasses per-owner RLS and MUST only be reached after a ROLE_PLATFORM_ADMIN
	// check in the app layer.
	ListOwners(ctx context.Context, query string, limit, offset int) ([]domain.Owner, int, error)
	GetBrand(ctx context.Context, ownerID, brandID string) (domain.Brand, error)
	ListBrands(ctx context.Context, ownerID string, limit int, offset int) ([]domain.Brand, int, error)
	ListRestaurants(ctx context.Context, ownerID, brandID string, limit, offset int) ([]domain.Restaurant, int, error)
	GetRestaurant(ctx context.Context, ownerID, restaurantID string) (domain.Restaurant, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent land in
// the same transaction (transactional outbox).
type Tx interface {
	InsertOwner(ctx context.Context, o domain.Owner) error
	InsertBrand(ctx context.Context, b domain.Brand) error
	InsertRestaurant(ctx context.Context, r domain.Restaurant) error
	UpdateBrand(ctx context.Context, b domain.Brand) error
	UpdateRestaurant(ctx context.Context, r domain.Restaurant) error

	GetBrand(ctx context.Context, brandID string) (domain.Brand, error)
	GetRestaurant(ctx context.Context, restaurantID string) (domain.Restaurant, error)

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType string, ownerID string, data any) error
}

// Entitlements is the port to the EntitlementsService (a generated client wraps
// it in the adapter). The app reserves quota before creating a constrained
// resource; over-limit => ResourceExhausted with the upgrade hint.
type Entitlements interface {
	// ReserveQuota atomically reserves `delta` of `key` for ownerID, idempotent by
	// reservationID. Returns ok=false (with an upgrade hint) when over limit.
	ReserveQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) (ReserveResult, error)
	// ReleaseQuota returns reserved quota (compensation on a failed create).
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

// BlobStore is the cloud-agnostic object-storage port for branding assets. The
// domain never sees AWS/GCS SDKs — only this interface and domain.Asset.
type BlobStore interface {
	Put(ctx context.Context, data []byte, contentType string) (domain.Asset, error)
}
