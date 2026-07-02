// Package auth signs and verifies short-lived Ed25519 JWT access tokens that
// carry the tenant scope (role + owner/brand/restaurant) claims used by the
// gRPC auth interceptor to populate tenancy context.
package auth

import (
	"crypto/ed25519"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
)

// Claims is the JWT payload: identity + tenant scope plus registered claims.
type Claims struct {
	UserID     string        `json:"uid"`
	Role       commonv1.Role `json:"role"`
	Owner      string        `json:"own,omitempty"`
	Brand      string        `json:"brnd,omitempty"`
	Restaurant string        `json:"rest,omitempty"`
	jwt.RegisteredClaims
}

// Sign issues a signed EdDSA token for c that expires after ttl.
func Sign(priv ed25519.PrivateKey, c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	if c.IssuedAt == nil {
		c.IssuedAt = jwt.NewNumericDate(now)
	}
	c.ExpiresAt = jwt.NewNumericDate(now.Add(ttl))
	tok := jwt.NewWithClaims(jwt.SigningMethodEdDSA, &c)
	signed, err := tok.SignedString(priv)
	if err != nil {
		return "", fmt.Errorf("auth: sign: %w", err)
	}
	return signed, nil
}

// Verify parses and validates token against pub, returning its Claims. It
// rejects tokens not signed with EdDSA and expired/invalid tokens.
func Verify(pub ed25519.PublicKey, token string) (Claims, error) {
	var c Claims
	parsed, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodEd25519); !ok {
			return nil, fmt.Errorf("auth: unexpected signing method %q", t.Header["alg"])
		}
		return pub, nil
	}, jwt.WithValidMethods([]string{"EdDSA"}))
	if err != nil {
		return Claims{}, fmt.Errorf("auth: verify: %w", err)
	}
	if !parsed.Valid {
		return Claims{}, fmt.Errorf("auth: token invalid")
	}
	return c, nil
}
