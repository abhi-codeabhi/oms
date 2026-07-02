// Package ports declares the interfaces the app layer depends on. Adapters
// (Postgres, in-memory fakes for tests) implement them; the app never imports an
// adapter directly. Dependency rule: app -> ports -> domain.
package ports

import (
	"context"

	"github.com/restorna/platform/services/entitlements/internal/domain"
)

// PlanRepo stores and retrieves plans (the catalog of quota/feature bundles).
type PlanRepo interface {
	GetPlan(ctx context.Context, planID string) (domain.Plan, error)
	UpsertPlan(ctx context.Context, p domain.Plan) (domain.Plan, error)
	// ListPlans returns the full plan catalog (platform-admin index). Plans are
	// global control-plane data, so this is not owner-scoped.
	ListPlans(ctx context.Context) ([]domain.Plan, error)
}

// EntitlementRepo stores and retrieves per-owner entitlements.
type EntitlementRepo interface {
	GetEntitlement(ctx context.Context, ownerID string) (domain.Entitlement, error)
	UpsertEntitlement(ctx context.Context, e domain.Entitlement) (domain.Entitlement, error)
}

// UsageRepo owns the transactionally-safe usage accounting: the current counter
// per (owner, key) and the reservation ledger used to dedupe by reservation_id.
//
// Reserve and Release MUST be atomic and idempotent by reservationID:
//   - Reserve: if reservationID already applied, return the prior result without
//     double-counting. Otherwise, if `limit` would be breached, return
//     domain.ErrQuotaExceeded and change nothing. Else record the reservation and
//     increment usage, returning the new remaining headroom.
//   - Release: if reservationID was never (or no longer) applied, it's a no-op
//     returning ok=true. Otherwise decrement usage and remove the ledger entry.
//
// `limit` is the effective limit (domain.Unlimited == -1 skips the cap check).
type UsageRepo interface {
	// Used returns the current usage counter for (owner, key); 0 if none.
	Used(ctx context.Context, ownerID, key string) (int64, error)

	// Reserve atomically applies +delta for (owner, key), deduped by
	// reservationID, enforcing limit. Returns the remaining headroom afterwards.
	Reserve(ctx context.Context, ownerID, key string, delta, limit int64, reservationID string) (remaining int64, err error)

	// Release atomically undoes a prior reservation identified by reservationID.
	Release(ctx context.Context, ownerID, key string, delta int64, reservationID string) error
}
