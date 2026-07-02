package bff

import (
	"net/http"

	"connectrpc.com/connect"

	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	tenantv1 "github.com/restorna/platform/gen/go/restorna/tenant/v1"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// --- /api/platform/* -> control-plane admin views (role: platform_admin) ---
//
// The M1 entitlements/tenant contracts expose per-owner reads + UpsertPlan (there
// is no ListOwners/ListPlans RPC yet), so these handlers cover: look up an owner,
// read an owner's effective plan/entitlement, and upsert a plan. Add list RPCs in a
// later proto rev to back a full owners/plans index.

// PlatformGetOwner GET /api/platform/owner?owner_id= -> TenantService.GetOwner.
func (b *BFF) PlatformGetOwner(w http.ResponseWriter, r *http.Request) {
	ownerID := r.URL.Query().Get("owner_id")
	if ownerID == "" {
		badRequest(w, "owner_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Tenant.GetOwner(ctx, connect.NewRequest(&tenantv1.GetOwnerRequest{OwnerId: ownerID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"owner": ownerJSON(resp.Msg.GetOwner())})
}

// PlatformGetEntitlement GET /api/platform/entitlement?owner_id= ->
// EntitlementsService.GetEntitlement (the owner's plan + effective quotas/features).
func (b *BFF) PlatformGetEntitlement(w http.ResponseWriter, r *http.Request) {
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

// PlatformUpsertPlan POST /api/platform/plan -> EntitlementsService.UpsertPlan.
func (b *BFF) PlatformUpsertPlan(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID       string           `json:"id"`
		Name     string           `json:"name"`
		Quotas   map[string]int64 `json:"quotas"`
		Features map[string]bool  `json:"features"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if in.ID == "" {
		badRequest(w, "id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Entitlements.UpsertPlan(ctx, connect.NewRequest(&entitlementsv1.UpsertPlanRequest{
		Plan: &entitlementsv1.Plan{
			Id:       in.ID,
			Name:     in.Name,
			Quotas:   in.Quotas,
			Features: in.Features,
		},
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"plan": planJSON(resp.Msg.GetPlan())})
}

// --- JSON projections ---

func ownerJSON(o *tenantv1.Owner) map[string]any {
	if o == nil {
		return nil
	}
	return map[string]any{
		"id":         o.GetId(),
		"name":       o.GetName(),
		"legal_name": o.GetLegalName(),
		"country":    o.GetCountry(),
		"created_at": o.GetCreatedAt(),
	}
}

func planJSON(p *entitlementsv1.Plan) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":       p.GetId(),
		"name":     p.GetName(),
		"quotas":   p.GetQuotas(),
		"features": p.GetFeatures(),
	}
}

func entitlementJSON(e *entitlementsv1.Entitlement) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"owner_id":          e.GetOwnerId(),
		"plan_id":           e.GetPlanId(),
		"quota_overrides":   e.GetQuotaOverrides(),
		"feature_overrides": e.GetFeatureOverrides(),
	}
}
