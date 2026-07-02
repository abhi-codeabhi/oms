// Package middleware holds the gateway's HTTP edge concerns: CORS, the auth
// requirement (verify JWT -> tenancy scope), rate limiting, and request logging.
// These wrap the BFF http.Handlers; they contain no business logic.
package middleware

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/auth"
	"github.com/restorna/platform/pkg/tenancy"
)

// ctxKeyToken carries the raw bearer token so BFF handlers can forward it to the
// downstream Connect clients (the gateway is a pass-through for authZ; backends
// re-verify and apply RLS).
type ctxKeyToken struct{}

// TokenFrom returns the raw bearer token placed on ctx by the auth middleware, if
// any. Handlers attach it to downstream requests via clients.WithToken.
func TokenFrom(ctx context.Context) (string, bool) {
	t, ok := ctx.Value(ctxKeyToken{}).(string)
	return t, ok && t != ""
}

// withToken stores the raw token on ctx.
func withToken(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, ctxKeyToken{}, token)
}

// Auth verifies the bearer JWT on each request using pubKey, places the resulting
// tenancy.Scope (and the raw token) on the request context, then calls next.
// Requests without a valid token are rejected 401. Mount only on protected routes;
// public routes (auth start/verify, customer-session, health) skip this.
type Auth struct {
	pub ed25519.PublicKey
}

// NewAuth builds the auth middleware. pubKey may be raw 32-byte material or
// base64 (std/url) encoded, matching pkg/grpcx.AuthInterceptor.
func NewAuth(pubKey string) *Auth {
	return &Auth{pub: decodePubKey(pubKey)}
}

// Require wraps next so it only runs for requests carrying a valid token.
func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.pub == nil {
			writeError(w, http.StatusInternalServerError, "auth: no public key configured")
			return
		}
		token := bearer(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		claims, err := auth.Verify(a.pub, token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "invalid token")
			return
		}
		scope := tenancy.Scope{
			OwnerID:      claims.Owner,
			BrandID:      claims.Brand,
			RestaurantID: claims.Restaurant,
			Role:         claims.Role,
			UserID:       claims.UserID,
		}
		ctx := withToken(tenancy.With(r.Context(), scope), token)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequireRole returns middleware that, after Require, also enforces that the
// scope's role is one of allowed (else 403). Compose as
// auth.Require(auth.RequireRole(h, ROLE_OWNER, ROLE_BRAND_ADMIN)).
func RequireRole(next http.Handler, allowed ...commonv1.Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scope, ok := tenancy.From(r.Context())
		if !ok {
			writeError(w, http.StatusUnauthorized, "no tenancy scope")
			return
		}
		if err := scope.Require(allowed...); err != nil {
			writeError(w, http.StatusForbidden, "role not permitted")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// bearer extracts the token from the Authorization header.
func bearer(r *http.Request) string {
	return strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer"))
}

func decodePubKey(s string) ed25519.PublicKey {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, dec := range []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
	} {
		if b, err := dec(s); err == nil && len(b) == ed25519.PublicKeySize {
			return ed25519.PublicKey(b)
		}
	}
	if len(s) == ed25519.PublicKeySize {
		return ed25519.PublicKey([]byte(s))
	}
	return nil
}

// writeError emits a JSON {"error": msg} body with the given status. Shared by the
// middleware and BFF handlers.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
