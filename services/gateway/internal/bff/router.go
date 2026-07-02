package bff

import (
	"net/http"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/services/gateway/internal/middleware"
)

// Router wires the BFF route groups onto a mux, applying the auth requirement and
// per-group role gates. Public routes (auth start/verify, customer-session) skip
// auth; everything else requires a valid token, and each surface enforces its role.
type Router struct {
	bff  *BFF
	auth *middleware.Auth
}

// NewRouter builds the route wiring around the BFF handlers and auth middleware.
func NewRouter(bff *BFF, auth *middleware.Auth) *Router {
	return &Router{bff: bff, auth: auth}
}

// Mount registers all /api/* routes on mux. The caller wraps the returned handler
// chain with CORS, rate limit, and logging at the server level.
func (rt *Router) Mount(mux *http.ServeMux) {
	b := rt.bff

	// --- /api/auth/* : PUBLIC for start/verify/customer-session; the rest take a
	// token in the body, so none require the auth middleware. ---
	mux.HandleFunc("POST /api/auth/start-otp", b.StartOTP)
	mux.HandleFunc("POST /api/auth/verify-otp", b.VerifyOTP)
	mux.HandleFunc("POST /api/auth/refresh", b.Refresh)
	mux.HandleFunc("POST /api/auth/customer-session", b.CustomerSession)
	// scoped-token needs the caller authenticated (forwarded to identity).
	mux.Handle("POST /api/auth/scoped-token", rt.protected(http.HandlerFunc(b.ScopedToken)))

	// --- /api/me : any authenticated principal ---
	mux.Handle("GET /api/me", rt.protected(http.HandlerFunc(b.Me)))

	// --- /api/platform/* : platform_admin only ---
	platform := func(h http.HandlerFunc) http.Handler {
		return rt.protectedRole(http.HandlerFunc(h), commonv1.Role_ROLE_PLATFORM_ADMIN)
	}
	mux.Handle("GET /api/platform/owner", platform(b.PlatformGetOwner))
	mux.Handle("GET /api/platform/entitlement", platform(b.PlatformGetEntitlement))
	mux.Handle("POST /api/platform/plan", platform(b.PlatformUpsertPlan))

	// --- /api/owner/* : owner or brand_admin ---
	owner := func(h http.HandlerFunc) http.Handler {
		return rt.protectedRole(http.HandlerFunc(h), commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN)
	}
	mux.Handle("POST /api/owner/onboarding/start", owner(b.OwnerStartOnboarding))
	mux.Handle("POST /api/owner/onboarding/submit-brand", owner(b.OwnerSubmitBrand))
	mux.Handle("POST /api/owner/onboarding/submit-outlet", owner(b.OwnerSubmitOutlet))
	mux.Handle("POST /api/owner/onboarding/invite-team", owner(b.OwnerInviteTeam))
	mux.Handle("POST /api/owner/onboarding/complete", owner(b.OwnerComplete))
	mux.Handle("GET /api/owner/onboarding/state", owner(b.OwnerOnboardingState))
	mux.Handle("GET /api/owner/brands", owner(b.OwnerListBrands))
	mux.Handle("GET /api/owner/outlets", owner(b.OwnerListOutlets))
	mux.Handle("GET /api/owner/entitlement", owner(b.OwnerGetEntitlement))
	mux.Handle("GET /api/owner/settings", owner(b.OwnerGetSettings))
	mux.Handle("POST /api/owner/settings", owner(b.OwnerSetSetting))

	// --- /api/manager/* : manager or owner ---
	manager := func(h http.HandlerFunc) http.Handler {
		return rt.protectedRole(http.HandlerFunc(h), commonv1.Role_ROLE_MANAGER, commonv1.Role_ROLE_OWNER)
	}
	mux.Handle("POST /api/manager/staff", manager(b.ManagerAddStaff))
	mux.Handle("GET /api/manager/staff", manager(b.ManagerListStaff))
	mux.Handle("POST /api/manager/staff/disable", manager(b.ManagerDisableStaff))
	mux.Handle("POST /api/manager/staff/change-role", manager(b.ManagerChangeRole))
	mux.Handle("POST /api/manager/staff/invite", manager(b.ManagerInviteStaff))
	mux.Handle("GET /api/manager/settings", manager(b.ManagerGetSettings))
	mux.Handle("POST /api/manager/settings", manager(b.ManagerSetSetting))
}

// protected requires a valid token (auth middleware) before h runs.
func (rt *Router) protected(h http.Handler) http.Handler {
	return rt.auth.Require(h)
}

// protectedRole requires a valid token AND one of the allowed roles.
func (rt *Router) protectedRole(h http.Handler, allowed ...commonv1.Role) http.Handler {
	return rt.auth.Require(middleware.RequireRole(h, allowed...))
}
