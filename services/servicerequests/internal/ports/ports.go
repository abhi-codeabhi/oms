// Package ports declares the interfaces the app layer depends on. Adapters
// (pg repo, settings client) implement them; unit tests supply in-memory fakes.
// The app NEVER imports an adapter directly (CONVENTIONS.md dependency rule:
// adapters -> app -> domain).
package ports

import (
	"context"
	"time"

	"github.com/restorna/platform/services/servicerequests/internal/domain"
)

// Repository is the persistence port for the Request aggregate plus the per
// table+type acknowledge-cooldown record. Implementations must scope every
// read/write to the restaurant via RLS (app.tenant_id), keyed by restaurant_id
// (service-requests is per-outlet).
type Repository interface {
	// Atomic runs fn inside a single transaction scoped to restaurantID (RLS).
	// Business writes + staged outbox events commit atomically.
	Atomic(ctx context.Context, restaurantID string, fn func(Tx) error) error

	// List returns every request for the restaurant (open/escalation views are
	// derived in the domain from this set).
	List(ctx context.Context, restaurantID string) ([]domain.Request, error)

	// LastAck returns the last acknowledge time recorded for a table+type, or the
	// zero time if the table+type was never acknowledged (no cooldown active).
	LastAck(ctx context.Context, restaurantID string, table int32, typ domain.Type) (time.Time, error)
}

// Tx is the unit-of-work handed to Atomic's callback. Writes + StageEvent +
// cooldown bumps land in the same transaction (transactional outbox).
type Tx interface {
	// Get loads a request by id within this tx (RLS-scoped). Returns
	// domain.ErrNotFound if it does not exist for the restaurant.
	Get(ctx context.Context, requestID string) (domain.Request, error)
	// Insert persists a brand-new request.
	Insert(ctx context.Context, r domain.Request) error
	// Update persists changes to an existing request (state/ackedAt).
	Update(ctx context.Context, r domain.Request) error

	// SetLastAck records (upserts) the acknowledge time for a table+type so the
	// next raise of that table+type is rate-limited until the cooldown elapses.
	SetLastAck(ctx context.Context, restaurantID string, table int32, typ domain.Type, at time.Time) error

	// StageEvent writes a CloudEvents row to the outbox in this same tx.
	StageEvent(ctx context.Context, eventType, restaurantID string, data any) error
}

// SettingsResolver reads effective settings for a restaurant scope from
// SettingsService.GetEffective. The settings client adapter implements it; unit
// tests use a fake. The app reads floor.call.cooldown_secs and the escalation
// threshold through this port and falls back to defaults when settings are
// unavailable.
type SettingsResolver interface {
	// Thresholds resolves the cooldown + escalation windows for the restaurant.
	// Implementations MUST return the package defaults (DefaultCooldown /
	// DefaultEscalation) for any key they cannot resolve, and must not error the
	// whole request just because settings are down (degrade to defaults).
	Thresholds(ctx context.Context, restaurantID string) (Thresholds, error)
}

// Thresholds carries the resolved rate-limit + escalation windows.
type Thresholds struct {
	Cooldown   time.Duration // floor.call.cooldown_secs (default 60s)
	Escalation time.Duration // floor.call.escalate_secs (default 30s)
}

// Defaults applied when SettingsService is unavailable (CONVENTIONS / task spec).
const (
	DefaultCooldown   = 60 * time.Second
	DefaultEscalation = 30 * time.Second
)
