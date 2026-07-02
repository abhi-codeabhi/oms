// Package ports declares the interfaces the app layer depends on. Concrete
// implementations live in adapters/ and are wired in cmd/server/main.go. The app
// never imports pgx/connect/nats directly — only these ports.
package ports

import (
	"context"

	"github.com/restorna/platform/services/staff/internal/domain"
)

// Repo persists the staff roster. Implementations are tenant-scoped via RLS
// (see adapters/pg); the app passes the tenant id so the adapter can set
// app.tenant_id on its transaction.
type Repo interface {
	// Create persists a new member within the tenant's transaction. If publish
	// is non-nil it is staged in the SAME transaction (transactional outbox).
	Create(ctx context.Context, tenantID string, m domain.Member, publish *OutboxEvent) error
	// Get loads a member by id (RLS ensures it belongs to the tenant).
	Get(ctx context.Context, tenantID, staffID string) (domain.Member, error)
	// Update persists changes to an existing member, optionally staging an event.
	Update(ctx context.Context, tenantID string, m domain.Member, publish *OutboxEvent) error
	// ListByRestaurant returns members for an outlet, paged.
	ListByRestaurant(ctx context.Context, tenantID, restaurantID string, limit int, offset int) (members []domain.Member, total int, err error)
}

// OutboxEvent is a domain event to be written transactionally with a state
// change. Type is the CloudEvents type, e.g. "restorna.staff.member.added.v1";
// Data is the JSON-serialisable payload.
type OutboxEvent struct {
	Type string
	Data any
}

// Entitlements is the port to the entitlements control-plane service. The pg
// nothing; it is a remote gRPC client in production and a fake in tests.
type Entitlements interface {
	// Reserve atomically reserves `delta` of `key` for the owner, idempotent by
	// reservationID. ok=false means the plan limit is reached; upgradeHint is a
	// human string to surface to the owner.
	Reserve(ctx context.Context, ownerID, key string, delta int64, reservationID string) (ok bool, upgradeHint string, err error)
	// Release returns reserved quota, idempotent by reservationID.
	Release(ctx context.Context, ownerID, key string, delta int64, reservationID string) error
}

// InviteSender delivers an invitation to a prospective staff member (email/SMS/
// WhatsApp via the notifications service in production). It is a port so the app
// can be tested with a fake.
type InviteSender interface {
	Send(ctx context.Context, inv domain.Invite) error
}
