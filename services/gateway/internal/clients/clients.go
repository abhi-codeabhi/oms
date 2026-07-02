// Package clients holds typed wrappers over the generated Connect clients for the
// backend control-plane services the gateway routes to. The BFF handlers depend on
// the small interfaces declared here (not the generated clients directly) so they
// can be unit-tested with fakes. This is the only package in the gateway that
// imports the generated *v1connect client packages.
package clients

import (
	"net/http"

	"connectrpc.com/connect"

	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	"github.com/restorna/platform/gen/go/restorna/identity/v1/identityv1connect"
	"github.com/restorna/platform/gen/go/restorna/onboarding/v1/onboardingv1connect"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/gen/go/restorna/staff/v1/staffv1connect"
	"github.com/restorna/platform/gen/go/restorna/tenant/v1/tenantv1connect"
)

// Set bundles one Connect client per backend service. Handlers receive the Set and
// call the typed client they need. The interface types are the generated
// *ServiceClient interfaces, so a fake (in tests) or the real client both satisfy
// them.
type Set struct {
	Identity     identityv1connect.IdentityServiceClient
	Tenant       tenantv1connect.TenantServiceClient
	Entitlements entitlementsv1connect.EntitlementsServiceClient
	Staff        staffv1connect.StaffServiceClient
	Settings     settingsv1connect.SettingsServiceClient
	Onboarding   onboardingv1connect.OnboardingServiceClient
}

// URLs holds the base URL of each backend service (from env), e.g.
// IDENTITY_URL="http://identity:8080".
type URLs struct {
	Identity     string
	Tenant       string
	Entitlements string
	Staff        string
	Settings     string
	Onboarding   string
}

// New builds a Set of real Connect clients over httpClient (which must speak h2c so
// gRPC/Connect work). opts are applied to every client (e.g. interceptors that
// forward the caller's bearer token downstream).
func New(httpClient *http.Client, u URLs, opts ...connect.ClientOption) *Set {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Set{
		Identity:     identityv1connect.NewIdentityServiceClient(httpClient, u.Identity, opts...),
		Tenant:       tenantv1connect.NewTenantServiceClient(httpClient, u.Tenant, opts...),
		Entitlements: entitlementsv1connect.NewEntitlementsServiceClient(httpClient, u.Entitlements, opts...),
		Staff:        staffv1connect.NewStaffServiceClient(httpClient, u.Staff, opts...),
		Settings:     settingsv1connect.NewSettingsServiceClient(httpClient, u.Settings, opts...),
		Onboarding:   onboardingv1connect.NewOnboardingServiceClient(httpClient, u.Onboarding, opts...),
	}
}
