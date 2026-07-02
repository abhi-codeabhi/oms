// Package auth adapts the shared pkg/auth Ed25519 JWT helpers to the app's
// Signer port, mapping the domain TokenGrant <-> auth.Claims.
package auth

import (
	"crypto/ed25519"
	"time"

	"github.com/restorna/platform/pkg/auth"
	"github.com/restorna/platform/services/identity/internal/domain"
	"github.com/restorna/platform/services/identity/internal/ports"
)

// Signer wraps an Ed25519 key pair. The private key signs; the public key
// verifies (also used by every other service's auth interceptor).
type Signer struct {
	priv ed25519.PrivateKey
	pub  ed25519.PublicKey
}

// New builds a Signer from an Ed25519 key pair.
func New(priv ed25519.PrivateKey, pub ed25519.PublicKey) *Signer {
	return &Signer{priv: priv, pub: pub}
}

// Sign mints a JWT for the grant with the given TTL.
func (s *Signer) Sign(g domain.TokenGrant, ttl time.Duration) (string, error) {
	return auth.Sign(s.priv, claimsFor(g), ttl)
}

// Verify validates a token and returns the asserted grant.
func (s *Signer) Verify(token string) (domain.TokenGrant, error) {
	c, err := auth.Verify(s.pub, token)
	if err != nil {
		return domain.TokenGrant{}, err
	}
	return domain.TokenGrant{
		UserID: c.UserID,
		Role:   c.Role,
		Scope: domain.TenantScope{
			OwnerID:      c.Owner,
			BrandID:      c.Brand,
			RestaurantID: c.Restaurant,
		},
	}, nil
}

// claimsFor maps a domain grant to the shared JWT claims.
func claimsFor(g domain.TokenGrant) auth.Claims {
	return auth.Claims{
		UserID:     g.UserID,
		Role:       g.Role,
		Owner:      g.Scope.OwnerID,
		Brand:      g.Scope.BrandID,
		Restaurant: g.Scope.RestaurantID,
	}
}

var _ ports.Signer = (*Signer)(nil)
