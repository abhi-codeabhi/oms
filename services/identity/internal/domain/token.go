package domain

import (
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
)

// Token lifetimes. Access tokens are short-lived; refresh tokens rotate.
const (
	AccessTTL  = 15 * time.Minute
	RefreshTTL = 30 * 24 * time.Hour
)

// TenantScope is a tenancy target carried in a scoped token.
type TenantScope struct {
	OwnerID      string
	BrandID      string
	RestaurantID string
}

// TokenGrant is the pure description of what a token should assert: who the
// subject is, their role, and their tenant scope. The app layer feeds this to
// the Signer port (pkg/auth) to mint an actual JWT — the domain never signs.
type TokenGrant struct {
	UserID string
	Role   commonv1.Role
	Scope  TenantScope
}

// UnscopedGrant is the grant issued at first login: subject + realm-default
// role, no tenant scope yet.
func UnscopedGrant(u User) TokenGrant {
	return TokenGrant{UserID: u.ID, Role: RoleForRealm(u.Realm)}
}

// ScopedGrant narrows a subject to a concrete tenant + role (after the user
// picks an outlet, or onboarding assigns one).
func ScopedGrant(userID string, role commonv1.Role, scope TenantScope) TokenGrant {
	return TokenGrant{UserID: userID, Role: role, Scope: scope}
}

// CustomerGrant builds an anonymous customer session bound to one restaurant +
// table with ROLE_CUSTOMER. The synthetic subject encodes the table
// ("<subject>:<table>") so the gateway/ordering can attribute the QR session
// even though the JWT scope only carries the restaurant id.
func CustomerGrant(subject, restaurantID, table string) TokenGrant {
	sub := subject
	if table != "" {
		sub = subject + ":" + table
	}
	return TokenGrant{
		UserID: sub,
		Role:   commonv1.Role_ROLE_CUSTOMER,
		Scope:  TenantScope{RestaurantID: restaurantID},
	}
}

// RefreshToken is a rotating opaque credential. The raw token string is hashed
// before storage (the app layer stores only TokenHash); the plaintext is
// returned to the client once.
type RefreshToken struct {
	ID        string
	UserID    string
	TokenHash string
	Role      commonv1.Role
	Scope     TenantScope
	ExpiresAt time.Time
	CreatedAt time.Time
	Revoked   bool
}

// NewRefreshToken builds a refresh record for a user that mirrors the grant's
// role + scope so a Refresh can re-mint an equivalent access token.
func NewRefreshToken(id, userID, tokenHash string, g TokenGrant, now time.Time) RefreshToken {
	return RefreshToken{
		ID:        id,
		UserID:    userID,
		TokenHash: tokenHash,
		Role:      g.Role,
		Scope:     g.Scope,
		ExpiresAt: now.Add(RefreshTTL),
		CreatedAt: now,
	}
}

// Usable reports whether the refresh token may still be exchanged.
func (r RefreshToken) Usable(now time.Time) bool {
	return !r.Revoked && now.Before(r.ExpiresAt)
}

// GrantFor reconstructs the token grant a refresh token should produce.
func (r RefreshToken) GrantFor() TokenGrant {
	return TokenGrant{UserID: r.UserID, Role: r.Role, Scope: r.Scope}
}
