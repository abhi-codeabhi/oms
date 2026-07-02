package bff

import (
	"encoding/base64"
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	tenantv1 "github.com/restorna/platform/gen/go/restorna/tenant/v1"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// --- /api/owner/* -> onboarding + tenant + entitlements + settings ---
// (role: owner / brand_admin). Handlers forward the caller's token; the backends
// resolve the trusted owner/brand from that scope, never the request body.

// OwnerStartOnboarding POST /api/owner/onboarding/start -> OnboardingService.StartOnboarding.
func (b *BFF) OwnerStartOnboarding(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OwnerName    string `json:"owner_name"`
		ContactEmail string `json:"contact_email"`
		ContactPhone string `json:"contact_phone"`
		Country      string `json:"country"`
		PlanID       string `json:"plan_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.StartOnboarding(ctx, connect.NewRequest(&onboardingv1.StartOnboardingRequest{
		OwnerName:    in.OwnerName,
		ContactEmail: in.ContactEmail,
		ContactPhone: in.ContactPhone,
		Country:      in.Country,
		PlanId:       in.PlanID,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState())})
}

// OwnerSubmitBrand POST /api/owner/onboarding/submit-brand -> OnboardingService.SubmitBrand.
// The logo is sent base64-encoded in JSON and decoded to bytes for the proto.
func (b *BFF) OwnerSubmitBrand(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OnboardingID    string `json:"onboarding_id"`
		BrandName       string `json:"brand_name"`
		PrimaryColor    string `json:"primary_color"`
		LogoBase64      string `json:"logo_base64"`
		LogoContentType string `json:"logo_content_type"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	var logo []byte
	if in.LogoBase64 != "" {
		b64, err := base64.StdEncoding.DecodeString(in.LogoBase64)
		if err != nil {
			badRequest(w, "logo_base64 not valid base64")
			return
		}
		logo = b64
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.SubmitBrand(ctx, connect.NewRequest(&onboardingv1.SubmitBrandRequest{
		OnboardingId:    in.OnboardingID,
		BrandName:       in.BrandName,
		PrimaryColor:    in.PrimaryColor,
		Logo:            logo,
		LogoContentType: in.LogoContentType,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState()), "brand_id": resp.Msg.GetBrandId()})
}

// OwnerSubmitOutlet POST /api/owner/onboarding/submit-outlet -> OnboardingService.SubmitOutlet.
func (b *BFF) OwnerSubmitOutlet(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OnboardingID string `json:"onboarding_id"`
		Name         string `json:"name"`
		Address      string `json:"address"`
		Timezone     string `json:"timezone"`
		GSTIN        string `json:"gstin"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.SubmitOutlet(ctx, connect.NewRequest(&onboardingv1.SubmitOutletRequest{
		OnboardingId: in.OnboardingID,
		Name:         in.Name,
		Address:      in.Address,
		Timezone:     in.Timezone,
		Gstin:        in.GSTIN,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState()), "restaurant_id": resp.Msg.GetRestaurantId()})
}

// OwnerInviteTeam POST /api/owner/onboarding/invite-team -> OnboardingService.InviteTeam.
func (b *BFF) OwnerInviteTeam(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OnboardingID string `json:"onboarding_id"`
		Invites      []struct {
			Name  string `json:"name"`
			Email string `json:"email"`
			Phone string `json:"phone"`
			Role  string `json:"role"`
		} `json:"invites"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	invites := make([]*onboardingv1.Invite, 0, len(in.Invites))
	for _, iv := range in.Invites {
		invites = append(invites, &onboardingv1.Invite{
			Name: iv.Name, Email: iv.Email, Phone: iv.Phone, Role: iv.Role,
		})
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.InviteTeam(ctx, connect.NewRequest(&onboardingv1.InviteTeamRequest{
		OnboardingId: in.OnboardingID,
		Invites:      invites,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState())})
}

// OwnerComplete POST /api/owner/onboarding/complete -> OnboardingService.Complete.
func (b *BFF) OwnerComplete(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OnboardingID string `json:"onboarding_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.Complete(ctx, connect.NewRequest(&onboardingv1.CompleteRequest{
		OnboardingId: in.OnboardingID,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState())})
}

// OwnerOnboardingState GET /api/owner/onboarding/state?onboarding_id= -> OnboardingService.GetState.
func (b *BFF) OwnerOnboardingState(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("onboarding_id")
	if id == "" {
		badRequest(w, "onboarding_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Onboarding.GetState(ctx, connect.NewRequest(&onboardingv1.GetStateRequest{OnboardingId: id}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"state": onbStateJSON(resp.Msg.GetState())})
}

// OwnerListBrands GET /api/owner/brands?owner_id= -> TenantService.ListBrands.
func (b *BFF) OwnerListBrands(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Tenant.ListBrands(ctx, connect.NewRequest(&tenantv1.ListBrandsRequest{OwnerId: ownerID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	brands := make([]map[string]any, 0, len(resp.Msg.GetBrands()))
	for _, br := range resp.Msg.GetBrands() {
		brands = append(brands, brandJSON(br))
	}
	writeJSON(w, map[string]any{"brands": brands})
}

// OwnerListOutlets GET /api/owner/outlets?brand_id= -> TenantService.ListRestaurants.
func (b *BFF) OwnerListOutlets(w http.ResponseWriter, r *http.Request) {
	brandID := r.URL.Query().Get("brand_id")
	if brandID == "" {
		badRequest(w, "brand_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Tenant.ListRestaurants(ctx, connect.NewRequest(&tenantv1.ListRestaurantsRequest{BrandId: brandID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	outlets := make([]map[string]any, 0, len(resp.Msg.GetRestaurants()))
	for _, o := range resp.Msg.GetRestaurants() {
		outlets = append(outlets, restaurantJSON(o))
	}
	writeJSON(w, map[string]any{"outlets": outlets})
}

// OwnerGetEntitlement GET /api/owner/entitlement?owner_id= -> EntitlementsService.GetEntitlement.
func (b *BFF) OwnerGetEntitlement(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		badRequest(w, "owner_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Entitlements.GetEntitlement(ctx, connect.NewRequest(&entitlementsv1.GetEntitlementRequest{OwnerId: ownerID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"entitlement":    entitlementJSON(resp.Msg.GetEntitlement()),
		"effective_plan": planJSON(resp.Msg.GetEffectivePlan()),
	})
}

// OwnerGetSettings GET /api/owner/settings?namespace=&keys=a,b -> SettingsService.GetEffective.
func (b *BFF) OwnerGetSettings(w http.ResponseWriter, r *http.Request) {
	b.getSettings(w, r)
}

// OwnerSetSetting POST /api/owner/settings -> SettingsService.SetOverride.
func (b *BFF) OwnerSetSetting(w http.ResponseWriter, r *http.Request) {
	b.setSetting(w, r)
}

// --- JSON projections ---

func onbStateJSON(s *onboardingv1.OnboardingState) map[string]any {
	if s == nil {
		return nil
	}
	completed := make([]string, 0, len(s.GetCompleted()))
	for _, st := range s.GetCompleted() {
		completed = append(completed, st.String())
	}
	return map[string]any{
		"id":        s.GetId(),
		"owner_id":  s.GetOwnerId(),
		"current":   s.GetCurrent().String(),
		"completed": completed,
		"done":      s.GetDone(),
	}
}

func brandJSON(b *tenantv1.Brand) map[string]any {
	if b == nil {
		return nil
	}
	return map[string]any{
		"id":            b.GetId(),
		"owner_id":      b.GetOwnerId(),
		"name":          b.GetName(),
		"logo":          assetJSON(b.GetLogo()),
		"primary_color": b.GetPrimaryColor(),
		"created_at":    b.GetCreatedAt(),
	}
}

func restaurantJSON(o *tenantv1.Restaurant) map[string]any {
	if o == nil {
		return nil
	}
	return map[string]any{
		"id":         o.GetId(),
		"brand_id":   o.GetBrandId(),
		"owner_id":   o.GetOwnerId(),
		"name":       o.GetName(),
		"address":    o.GetAddress(),
		"timezone":   o.GetTimezone(),
		"gstin":      o.GetGstin(),
		"logo":       assetJSON(o.GetLogo()),
		"active":     o.GetActive(),
		"created_at": o.GetCreatedAt(),
	}
}

func assetJSON(a *commonv1.Asset) map[string]any {
	if a == nil {
		return nil
	}
	return map[string]any{"id": a.GetId(), "url": a.GetUrl(), "content_type": a.GetContentType()}
}

// settingsValuesJSON projects resolved setting values for the settings BFFs.
func settingsValuesJSON(vals []*settingsv1.SettingValue) []map[string]any {
	out := make([]map[string]any, 0, len(vals))
	for _, sv := range vals {
		m := map[string]any{
			"key":          sv.GetKey(),
			"source_scope": sv.GetSourceScope().String(),
		}
		if v := sv.GetValue(); v != nil {
			m["type"] = v.GetType().String()
			m["raw"] = v.GetRaw()
		}
		out = append(out, m)
	}
	return out
}
