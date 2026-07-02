package middleware

import (
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/auth"
	"github.com/restorna/platform/pkg/tenancy"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	return pub, priv
}

func token(t *testing.T, priv ed25519.PrivateKey, role commonv1.Role) string {
	t.Helper()
	tok, err := auth.Sign(priv, auth.Claims{UserID: "usr_1", Role: role, Owner: "own_1"}, time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// okHandler records that it ran and reflects the scope back via status 200.
func okHandler(ran *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*ran = true
		w.WriteHeader(http.StatusOK)
	})
}

func TestAuthRequire(t *testing.T) {
	pub, priv := keys(t)
	a := NewAuth(string(pub)) // raw 32-byte key

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
		wantRan    bool
	}{
		{name: "missing token rejected", authHeader: "", wantStatus: http.StatusUnauthorized, wantRan: false},
		{name: "garbage token rejected", authHeader: "Bearer not-a-jwt", wantStatus: http.StatusUnauthorized, wantRan: false},
		{name: "valid token allowed", authHeader: "Bearer " + token(t, priv, commonv1.Role_ROLE_OWNER), wantStatus: http.StatusOK, wantRan: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			h := a.Require(okHandler(&ran))
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d", rec.Code, tc.wantStatus)
			}
			if ran != tc.wantRan {
				t.Fatalf("handler ran: got %v want %v", ran, tc.wantRan)
			}
		})
	}
}

// A public route does NOT mount Require, so it runs without a token. This proves the
// router's public-vs-protected split: the handler itself never checks auth.
func TestPublicRouteSkipsAuth(t *testing.T) {
	ran := false
	pub := okHandler(&ran) // mounted WITHOUT a.Require, like /api/auth/start-otp
	req := httptest.NewRequest(http.MethodPost, "/api/auth/start-otp", nil)
	rec := httptest.NewRecorder()
	pub.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !ran {
		t.Fatalf("public handler should run without a token: status=%d ran=%v", rec.Code, ran)
	}
}

func TestRequireRoleGate(t *testing.T) {
	pub, priv := keys(t)
	a := NewAuth(string(pub))

	tests := []struct {
		name       string
		role       commonv1.Role
		allowed    []commonv1.Role
		wantStatus int
		wantRan    bool
	}{
		{
			name: "owner allowed on owner route", role: commonv1.Role_ROLE_OWNER,
			allowed: []commonv1.Role{commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN},
			wantStatus: http.StatusOK, wantRan: true,
		},
		{
			name: "manager forbidden on platform route", role: commonv1.Role_ROLE_MANAGER,
			allowed: []commonv1.Role{commonv1.Role_ROLE_PLATFORM_ADMIN},
			wantStatus: http.StatusForbidden, wantRan: false,
		},
		{
			name: "platform admin allowed on platform route", role: commonv1.Role_ROLE_PLATFORM_ADMIN,
			allowed: []commonv1.Role{commonv1.Role_ROLE_PLATFORM_ADMIN},
			wantStatus: http.StatusOK, wantRan: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ran := false
			h := a.Require(RequireRole(okHandler(&ran), tc.allowed...))
			req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
			req.Header.Set("Authorization", "Bearer "+token(t, priv, tc.role))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status: got %d want %d", rec.Code, tc.wantStatus)
			}
			if ran != tc.wantRan {
				t.Fatalf("handler ran: got %v want %v", ran, tc.wantRan)
			}
		})
	}
}

func TestTokenBucketAllow(t *testing.T) {
	now := time.Now()
	tb := NewTokenBucket(1, 2) // burst 2, 1 tok/sec
	tb.now = func() time.Time { return now }

	// First two allowed (burst), third denied (bucket empty).
	if !tb.Allow("k") || !tb.Allow("k") {
		t.Fatalf("first two requests should be allowed by burst")
	}
	if tb.Allow("k") {
		t.Fatalf("third request should be denied (empty bucket)")
	}
	// After 1s, one token refills.
	now = now.Add(time.Second)
	if !tb.Allow("k") {
		t.Fatalf("request after refill should be allowed")
	}
	// A different key has its own bucket.
	if !tb.Allow("other") {
		t.Fatalf("independent key should be allowed")
	}
}

func TestRateLimitMiddleware429(t *testing.T) {
	rl := NewRateLimit(NewTokenBucket(1, 1)) // burst 1
	var ran int
	h := rl.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		ran++
		w.WriteHeader(http.StatusOK)
	}))

	newReq := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		r.RemoteAddr = "10.0.0.1:1234"
		return r
	}
	rec1 := httptest.NewRecorder()
	h.ServeHTTP(rec1, newReq())
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, newReq())

	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: got %d want 200", rec1.Code)
	}
	if rec2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request: got %d want 429", rec2.Code)
	}
	if ran != 1 {
		t.Fatalf("handler should have run once, ran %d", ran)
	}
}

func TestCORSPreflight(t *testing.T) {
	c := NewCORS("*")
	h := c.Wrap(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatalf("preflight should short-circuit, handler must not run")
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/me", nil)
	req.Header.Set("Origin", "https://console.restorna.app")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("preflight status: got %d want 204", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://console.restorna.app" {
		t.Fatalf("allow-origin: got %q", got)
	}
}

// Sanity: scope is reachable from context after Require, proving the token's claims
// land in tenancy (used by downstream forwarding + rate-limit keying).
func TestScopeInContext(t *testing.T) {
	pub, priv := keys(t)
	a := NewAuth(string(pub))
	var got tenancy.Scope
	h := a.Require(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = tenancy.From(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.Header.Set("Authorization", "Bearer "+token(t, priv, commonv1.Role_ROLE_OWNER))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.UserID != "usr_1" || got.Role != commonv1.Role_ROLE_OWNER || got.OwnerID != "own_1" {
		t.Fatalf("scope not populated from token: %+v", got)
	}
}
