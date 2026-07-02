// Package ports declares the interfaces the onboarding app layer depends on.
// Concrete implementations live in adapters/ and are wired in
// cmd/server/main.go. The app never imports pgx/connect/nats directly — only
// these ports. The five downstream service clients are PORTS so the saga can be
// unit-tested against in-memory fakes that record calls.
package ports

import (
	"context"
	"errors"

	"github.com/restorna/platform/services/onboarding/internal/domain"
)

// ErrQuotaExhausted is returned by Staff.AddStaff when the plan's staff.<role>
// quota is reached (the staff service replies ResourceExhausted). The saga
// treats it as a per-invite failure to report, not a fatal step error.
var ErrQuotaExhausted = errors.New("staff quota exhausted")

// Repo persists the onboarding saga ledger (OnboardingState). It is tenant-scoped
// via RLS by owner_id (see adapters/pg). Until the ACCOUNT step assigns an owner
// id the saga is keyed by its own id within a bootstrap scope.
type Repo interface {
	// Create persists a brand-new saga state, optionally staging an event in the
	// SAME transaction (transactional outbox).
	Create(ctx context.Context, s domain.State, publish *OutboxEvent) error
	// Get loads a saga by id. Returns domain.ErrNotFound if absent.
	Get(ctx context.Context, onboardingID string) (domain.State, error)
	// Save upserts the saga ledger after a step advances, optionally staging an
	// event transactionally. Writing the step ledger here is what makes a retried
	// RPC idempotent.
	Save(ctx context.Context, s domain.State, publish *OutboxEvent) error
}

// OutboxEvent is a domain event written transactionally with a state change.
// Type is the CloudEvents type (e.g. "restorna.onboarding.completed.v1"); Data
// is the JSON-serialisable payload.
type OutboxEvent struct {
	Type string
	Data any
}

// Identity is the port to the identity service. Onboarding registers the owner's
// login so they can later authenticate. Idempotent by email/phone on the
// identity side; the saga stores the returned user id.
type Identity interface {
	// EnsureOwnerUser registers (or returns the existing) tenant-realm user for
	// the owner's email/phone and returns the user id.
	EnsureOwnerUser(ctx context.Context, email, phone, displayName string) (userID string, err error)
}

// Tenant is the port to the tenant service: owner -> brand -> restaurant plus
// branding. Each method maps to one RPC; the saga supplies the ids it has stored
// so retries reuse them.
type Tenant interface {
	// CreateOwner provisions the owner record and returns its id.
	CreateOwner(ctx context.Context, name, legalName, country string) (ownerID string, err error)
	// CreateBrand provisions the first brand under the owner.
	CreateBrand(ctx context.Context, ownerID, name, primaryColor string) (brandID string, err error)
	// SetBrandLogo uploads the brand logo bytes and returns the stored asset URL.
	SetBrandLogo(ctx context.Context, brandID string, logo []byte, contentType string) (assetURL string, err error)
	// CreateRestaurant provisions the first outlet under the brand.
	CreateRestaurant(ctx context.Context, brandID, name, address, timezone, gstin string) (restaurantID string, err error)
}

// Entitlements is the port to the entitlements service. Onboarding assigns the
// owner's plan via SetEntitlement (idempotent: re-setting the same plan is a
// no-op effect).
type Entitlements interface {
	// AssignPlan sets the owner's effective plan id.
	AssignPlan(ctx context.Context, ownerID, planID string) error
}

// Staff is the port to the staff service. Onboarding invites the first team via
// AddStaff + InviteStaff. AddStaff may be rejected by the plan's staff.<role>
// quota; the saga reports which invites failed rather than aborting the whole
// step. ErrQuotaExhausted distinguishes the over-limit case from a transport
// failure so the saga can surface an upgrade-appropriate message.
type Staff interface {
	// AddStaff creates a roster member for the outlet and returns its id.
	// Returns ErrQuotaExhausted (wrapped) when the plan's staff.<role> limit is
	// reached.
	AddStaff(ctx context.Context, restaurantID, name, email, phone, role string) (staffID string, err error)
	// InviteStaff sends the invite for an existing member.
	InviteStaff(ctx context.Context, staffID string) (inviteID string, err error)
}

// Settings is the port to the settings service. Onboarding seeds outlet defaults
// (currency, gst, etc.) via SetOverride at restaurant scope.
type Settings interface {
	// SetOverride writes one setting override for the given scope.
	SetOverride(ctx context.Context, ownerID, brandID, restaurantID, key, valueType, raw string) error
}
