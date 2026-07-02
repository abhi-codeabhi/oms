// Package app holds the entitlements use cases. It orchestrates the domain rules
// over the ports (repos) and is the only place transport adapters call into. It
// imports ports + domain only — never pgx/connect/nats.
package app

import (
	"context"
	"fmt"

	"github.com/restorna/platform/services/entitlements/internal/domain"
	"github.com/restorna/platform/services/entitlements/internal/ports"
)

// Service implements the entitlements use cases over its repositories.
type Service struct {
	plans   ports.PlanRepo
	ents    ports.EntitlementRepo
	usage   ports.UsageRepo
	hintFn  func(planID, key string) string
}

// New wires the service with its repositories.
func New(plans ports.PlanRepo, ents ports.EntitlementRepo, usage ports.UsageRepo) *Service {
	return &Service{plans: plans, ents: ents, usage: usage, hintFn: defaultUpgradeHint}
}

// QuotaResult is the outcome of a quota check.
type QuotaResult struct {
	Allowed     bool
	Remaining   int64
	Limit       int64
	UpgradeHint string
}

// effective loads an owner's entitlement, its plan, and merges them.
func (s *Service) effective(ctx context.Context, ownerID string) (domain.Entitlement, domain.Plan, error) {
	if ownerID == "" {
		return domain.Entitlement{}, domain.Plan{}, domain.ErrInvalid
	}
	ent, err := s.ents.GetEntitlement(ctx, ownerID)
	if err != nil {
		return domain.Entitlement{}, domain.Plan{}, err
	}
	plan, err := s.plans.GetPlan(ctx, ent.PlanID)
	if err != nil {
		return domain.Entitlement{}, domain.Plan{}, err
	}
	return ent, domain.EffectivePlan(plan, ent), nil
}

// GetEntitlement returns the owner's entitlement and their effective plan.
func (s *Service) GetEntitlement(ctx context.Context, ownerID string) (domain.Entitlement, domain.Plan, error) {
	return s.effective(ctx, ownerID)
}

// CheckQuota answers "may I create `delta` more of `key`?" without mutating
// state. remaining/limit reflect the effective plan and current usage.
func (s *Service) CheckQuota(ctx context.Context, ownerID, key string, delta int64) (QuotaResult, error) {
	if key == "" {
		return QuotaResult{}, domain.ErrInvalid
	}
	ent, plan, err := s.effective(ctx, ownerID)
	if err != nil {
		return QuotaResult{}, err
	}
	limit, governed := plan.Limit(key)
	if !governed {
		// Ungoverned keys are unconstrained.
		limit = domain.Unlimited
	}
	used, err := s.usage.Used(ctx, ownerID, key)
	if err != nil {
		return QuotaResult{}, err
	}
	res := QuotaResult{
		Allowed:   domain.Allows(limit, used, delta),
		Remaining: domain.Remaining(limit, used),
		Limit:     limit,
	}
	if !res.Allowed {
		res.UpgradeHint = s.hintFn(ent.PlanID, key)
	}
	return res, nil
}

// ReserveQuota atomically and idempotently reserves `delta` of `key`. It is
// deduped by reservationID; a second call with the same id returns the same
// result without double-counting. Over-limit returns domain.ErrQuotaExceeded.
func (s *Service) ReserveQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) (ok bool, remaining int64, err error) {
	if key == "" || reservationID == "" {
		return false, 0, domain.ErrInvalid
	}
	_, plan, err := s.effective(ctx, ownerID)
	if err != nil {
		return false, 0, err
	}
	limit, governed := plan.Limit(key)
	if !governed {
		limit = domain.Unlimited
	}
	remaining, err = s.usage.Reserve(ctx, ownerID, key, delta, limit, reservationID)
	if err != nil {
		return false, 0, err
	}
	return true, remaining, nil
}

// ReleaseQuota atomically and idempotently undoes a prior reservation by
// reservationID. Releasing an unknown/already-released reservation is a no-op.
func (s *Service) ReleaseQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) (bool, error) {
	if key == "" || reservationID == "" {
		return false, domain.ErrInvalid
	}
	if err := s.usage.Release(ctx, ownerID, key, delta, reservationID); err != nil {
		return false, err
	}
	return true, nil
}

// HasFeature reports whether a feature flag is enabled for the owner under their
// effective plan (overrides win).
func (s *Service) HasFeature(ctx context.Context, ownerID, feature string) (bool, error) {
	if feature == "" {
		return false, domain.ErrInvalid
	}
	_, plan, err := s.effective(ctx, ownerID)
	if err != nil {
		return false, err
	}
	return plan.HasFeature(feature), nil
}

// SetEntitlement assigns/updates an owner's plan + overrides (admin / billing).
func (s *Service) SetEntitlement(ctx context.Context, e domain.Entitlement) (domain.Entitlement, error) {
	if err := e.Validate(); err != nil {
		return domain.Entitlement{}, err
	}
	// Referential check: the plan must exist.
	if _, err := s.plans.GetPlan(ctx, e.PlanID); err != nil {
		return domain.Entitlement{}, err
	}
	return s.ents.UpsertEntitlement(ctx, e)
}

// UpsertPlan creates or replaces a plan (admin).
func (s *Service) UpsertPlan(ctx context.Context, p domain.Plan) (domain.Plan, error) {
	if err := p.Validate(); err != nil {
		return domain.Plan{}, err
	}
	return s.plans.UpsertPlan(ctx, p)
}

// ListPlans returns the full plan catalog (platform-admin index). Plans are
// global, not owner-scoped, so no quota/entitlement resolution is needed.
func (s *Service) ListPlans(ctx context.Context) ([]domain.Plan, error) {
	return s.plans.ListPlans(ctx)
}

func defaultUpgradeHint(planID, key string) string {
	return fmt.Sprintf("Plan %q has reached its %q limit. Upgrade your plan to add more.", planID, key)
}
