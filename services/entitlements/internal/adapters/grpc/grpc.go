// Package grpc is the Connect transport adapter: it implements the generated
// EntitlementsServiceHandler, maps proto messages <-> domain types, and converts
// domain errors to Connect codes (via pkg/errors). It is the only layer that
// knows about connect or the generated code.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"

	"github.com/restorna/platform/services/entitlements/internal/app"
	"github.com/restorna/platform/services/entitlements/internal/domain"
)

// Handler adapts the app.Service to the generated Connect handler interface.
type Handler struct {
	entitlementsv1connect.UnimplementedEntitlementsServiceHandler
	svc *app.Service
}

// New constructs a Connect handler around the use cases.
func New(svc *app.Service) *Handler { return &Handler{svc: svc} }

// GetEntitlement returns the owner's entitlement + effective plan.
func (h *Handler) GetEntitlement(ctx context.Context, req *connect.Request[entitlementsv1.GetEntitlementRequest]) (*connect.Response[entitlementsv1.GetEntitlementResponse], error) {
	ent, plan, err := h.svc.GetEntitlement(ctx, req.Msg.GetOwnerId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.GetEntitlementResponse{
		Entitlement:   toProtoEntitlement(ent),
		EffectivePlan: toProtoPlan(plan),
	}), nil
}

// CheckQuota reports whether delta more of key fits, plus remaining/limit/hint.
func (h *Handler) CheckQuota(ctx context.Context, req *connect.Request[entitlementsv1.CheckQuotaRequest]) (*connect.Response[entitlementsv1.CheckQuotaResponse], error) {
	res, err := h.svc.CheckQuota(ctx, req.Msg.GetOwnerId(), req.Msg.GetKey(), req.Msg.GetDelta())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.CheckQuotaResponse{
		Allowed:     res.Allowed,
		Remaining:   res.Remaining,
		Limit:       res.Limit,
		UpgradeHint: res.UpgradeHint,
	}), nil
}

// ReserveQuota atomically + idempotently reserves quota. Over-limit -> ResourceExhausted.
func (h *Handler) ReserveQuota(ctx context.Context, req *connect.Request[entitlementsv1.ReserveQuotaRequest]) (*connect.Response[entitlementsv1.ReserveQuotaResponse], error) {
	ok, remaining, err := h.svc.ReserveQuota(ctx, req.Msg.GetOwnerId(), req.Msg.GetKey(), req.Msg.GetDelta(), req.Msg.GetReservationId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.ReserveQuotaResponse{
		Ok:        ok,
		Remaining: remaining,
	}), nil
}

// ReleaseQuota atomically + idempotently releases a prior reservation.
func (h *Handler) ReleaseQuota(ctx context.Context, req *connect.Request[entitlementsv1.ReleaseQuotaRequest]) (*connect.Response[entitlementsv1.ReleaseQuotaResponse], error) {
	ok, err := h.svc.ReleaseQuota(ctx, req.Msg.GetOwnerId(), req.Msg.GetKey(), req.Msg.GetDelta(), req.Msg.GetReservationId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.ReleaseQuotaResponse{Ok: ok}), nil
}

// HasFeature reports whether a feature flag is enabled for the owner.
func (h *Handler) HasFeature(ctx context.Context, req *connect.Request[entitlementsv1.HasFeatureRequest]) (*connect.Response[entitlementsv1.HasFeatureResponse], error) {
	enabled, err := h.svc.HasFeature(ctx, req.Msg.GetOwnerId(), req.Msg.GetFeature())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.HasFeatureResponse{Enabled: enabled}), nil
}

// SetEntitlement assigns/updates an owner's plan + overrides (admin).
func (h *Handler) SetEntitlement(ctx context.Context, req *connect.Request[entitlementsv1.SetEntitlementRequest]) (*connect.Response[entitlementsv1.SetEntitlementResponse], error) {
	in := req.Msg.GetEntitlement()
	if in == nil {
		return nil, toConnect(domain.ErrInvalid)
	}
	saved, err := h.svc.SetEntitlement(ctx, fromProtoEntitlement(in))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.SetEntitlementResponse{
		Entitlement: toProtoEntitlement(saved),
	}), nil
}

// UpsertPlan creates/replaces a plan (admin).
func (h *Handler) UpsertPlan(ctx context.Context, req *connect.Request[entitlementsv1.UpsertPlanRequest]) (*connect.Response[entitlementsv1.UpsertPlanResponse], error) {
	in := req.Msg.GetPlan()
	if in == nil {
		return nil, toConnect(domain.ErrInvalid)
	}
	saved, err := h.svc.UpsertPlan(ctx, fromProtoPlan(in))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&entitlementsv1.UpsertPlanResponse{
		Plan: toProtoPlan(saved),
	}), nil
}

// ListPlans returns the full plan catalog (platform admin).
func (h *Handler) ListPlans(ctx context.Context, req *connect.Request[entitlementsv1.ListPlansRequest]) (*connect.Response[entitlementsv1.ListPlansResponse], error) {
	plans, err := h.svc.ListPlans(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*entitlementsv1.Plan, 0, len(plans))
	for _, p := range plans {
		out = append(out, toProtoPlan(p))
	}
	return connect.NewResponse(&entitlementsv1.ListPlansResponse{Plans: out}), nil
}

// ---- mapping helpers ------------------------------------------------------

func toProtoPlan(p domain.Plan) *entitlementsv1.Plan {
	return &entitlementsv1.Plan{
		Id:       p.ID,
		Name:     p.Name,
		Quotas:   p.Quotas,
		Features: p.Features,
	}
}

func fromProtoPlan(p *entitlementsv1.Plan) domain.Plan {
	return domain.Plan{
		ID:       p.GetId(),
		Name:     p.GetName(),
		Quotas:   p.GetQuotas(),
		Features: p.GetFeatures(),
	}
}

func toProtoEntitlement(e domain.Entitlement) *entitlementsv1.Entitlement {
	return &entitlementsv1.Entitlement{
		OwnerId:          e.OwnerID,
		PlanId:           e.PlanID,
		QuotaOverrides:   e.QuotaOverrides,
		FeatureOverrides: e.FeatureOverrides,
	}
}

func fromProtoEntitlement(e *entitlementsv1.Entitlement) domain.Entitlement {
	return domain.Entitlement{
		OwnerID:          e.GetOwnerId(),
		PlanID:           e.GetPlanId(),
		QuotaOverrides:   e.GetQuotaOverrides(),
		FeatureOverrides: e.GetFeatureOverrides(),
	}
}

// toConnect normalises domain errors to the shared pkg/errors sentinels so
// pkg/errors.ToConnect can assign the right Connect code (ResourceExhausted for
// quota, NotFound, InvalidArgument, ...).
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrQuotaExceeded):
		return pkgerrors.ToConnect(pkgerrors.ErrQuotaExceeded)
	case errors.Is(err, domain.ErrPlanNotFound), errors.Is(err, domain.ErrEntitlementNotFound):
		return pkgerrors.ToConnect(pkgerrors.ErrNotFound)
	case errors.Is(err, domain.ErrInvalid):
		return pkgerrors.ToConnect(pkgerrors.ErrInvalid)
	default:
		return pkgerrors.ToConnect(err)
	}
}
