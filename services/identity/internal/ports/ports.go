// Package ports declares the interfaces the app layer depends on. Adapters
// (pg, sender, auth signer) implement them; wiring happens only in main.go.
// The app imports ports + domain, never concrete adapters.
package ports

import (
	"context"
	"time"

	"github.com/restorna/platform/services/identity/internal/domain"
)

// UserRepo persists and looks up users. Identity is cross-tenant, so there is
// no tenant_id filter here; rows are scoped by realm + address instead.
type UserRepo interface {
	// FindByAddress returns the user matching an email/phone within a realm.
	// Returns domain.ErrUserNotFound when absent.
	FindByAddress(ctx context.Context, realm domain.Realm, address string) (domain.User, error)
	FindByID(ctx context.Context, id string) (domain.User, error)
	// Create inserts a new user.
	Create(ctx context.Context, u domain.User) error
}

// ChallengeRepo persists OTP challenges with their TTL + attempt state.
type ChallengeRepo interface {
	Save(ctx context.Context, c domain.OtpChallenge) error
	Get(ctx context.Context, id string) (domain.OtpChallenge, error)
	// Update persists a mutated challenge (attempt increment / consumed flag).
	Update(ctx context.Context, c domain.OtpChallenge) error
}

// RefreshRepo persists rotating refresh tokens (by hash).
type RefreshRepo interface {
	Save(ctx context.Context, t domain.RefreshToken) error
	// GetByHash returns a refresh token by its stored hash.
	GetByHash(ctx context.Context, hash string) (domain.RefreshToken, error)
	// Revoke marks a refresh token revoked (rotation / logout).
	Revoke(ctx context.Context, id string) error
}

// Sender is the pluggable OTP delivery port. The log/no-op impl ships now; the
// real SMS/email path is the notifications service later.
type Sender interface {
	Send(ctx context.Context, channel domain.Channel, address, code string) error
}

// Signer mints and verifies JWTs. Backed by pkg/auth (Ed25519). Kept as a port
// so the app never touches crypto directly and tests can fake it.
type Signer interface {
	// Sign issues a JWT for the grant with the given TTL.
	Sign(g domain.TokenGrant, ttl time.Duration) (string, error)
	// Verify parses + validates an access token, returning the asserted grant.
	Verify(token string) (domain.TokenGrant, error)
}

// Clock allows deterministic time in tests. Production uses RealClock.
type Clock interface{ Now() time.Time }

// RealClock returns the wall clock in UTC.
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now().UTC() }
