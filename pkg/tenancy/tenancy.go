// Package tenancy carries the JWT-derived tenant scope through request context.
//
// The Scope (owner/brand/restaurant + role + user) is set by the auth
// interceptor and read by services via From. Tenant ids are never trusted from
// a request body — only from the context placed here.
package tenancy

import (
	"context"
	"errors"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
)

// ErrPermissionDenied is returned by Require when the scope's role is not allowed.
var ErrPermissionDenied = errors.New("tenancy: permission denied")

// Scope is the tenant + identity context for a request.
type Scope struct {
	OwnerID      string
	BrandID      string
	RestaurantID string
	Role         commonv1.Role
	UserID       string
}

type ctxKey struct{}

// With returns a copy of ctx carrying s.
func With(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, ctxKey{}, s)
}

// From extracts the Scope from ctx; ok is false if none was set.
func From(ctx context.Context) (Scope, bool) {
	s, ok := ctx.Value(ctxKey{}).(Scope)
	return s, ok
}

// Require returns nil if the scope's role is in the allowed set, otherwise
// ErrPermissionDenied. With no roles given it requires only that a role is set.
func (s Scope) Require(role ...commonv1.Role) error {
	if len(role) == 0 {
		if s.Role == commonv1.Role_ROLE_UNSPECIFIED {
			return fmt.Errorf("%w: no role in scope", ErrPermissionDenied)
		}
		return nil
	}
	for _, r := range role {
		if s.Role == r {
			return nil
		}
	}
	return fmt.Errorf("%w: role %s not in allowed set", ErrPermissionDenied, s.Role)
}
