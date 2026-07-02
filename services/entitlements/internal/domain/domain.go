// Package domain holds the pure entitlements model and rules: plans (quotas +
// feature flags), per-owner entitlements with overrides, and the math that turns
// them into an effective plan plus quota decisions.
//
// It imports no infrastructure (no pgx/connect/nats). All quota numbers are
// int64; the sentinel -1 means "unlimited".
package domain

import "errors"

// Unlimited is the quota-limit sentinel meaning "no cap".
const Unlimited int64 = -1

// Domain errors. Adapters map these to transport codes (see pkg/errors).
var (
	// ErrPlanNotFound is returned when an owner references an unknown plan.
	ErrPlanNotFound = errors.New("entitlements: plan not found")
	// ErrEntitlementNotFound is returned when an owner has no entitlement row.
	ErrEntitlementNotFound = errors.New("entitlements: entitlement not found")
	// ErrQuotaExceeded is returned by a reservation that would breach a limit.
	ErrQuotaExceeded = errors.New("entitlements: quota exceeded")
	// ErrInvalid is returned for malformed input (empty owner, blank key, etc.).
	ErrInvalid = errors.New("entitlements: invalid argument")
)

// Plan is a named bundle of keyed quota limits and boolean feature flags. Plans
// are data (seeded + admin-editable), not code.
type Plan struct {
	ID       string
	Name     string
	Quotas   map[string]int64 // key -> limit; -1 = unlimited
	Features map[string]bool  // feature -> enabled
}

// Entitlement is an owner's assignment to a plan plus sales-negotiated overrides
// that win over the plan's defaults.
type Entitlement struct {
	OwnerID          string
	PlanID           string
	QuotaOverrides   map[string]int64
	FeatureOverrides map[string]bool
}

// Validate checks an entitlement is well-formed before persistence.
func (e Entitlement) Validate() error {
	if e.OwnerID == "" {
		return ErrInvalid
	}
	if e.PlanID == "" {
		return ErrInvalid
	}
	return nil
}

// Validate checks a plan is well-formed before persistence.
func (p Plan) Validate() error {
	if p.ID == "" {
		return ErrInvalid
	}
	return nil
}

// EffectivePlan merges a plan with an owner's overrides into the plan that
// actually governs that owner. Override entries replace plan entries key-by-key;
// keys present only in the plan are kept. The returned plan keeps the plan's id
// and name so callers can show "Pro (custom)".
func EffectivePlan(p Plan, e Entitlement) Plan {
	out := Plan{
		ID:       p.ID,
		Name:     p.Name,
		Quotas:   make(map[string]int64, len(p.Quotas)+len(e.QuotaOverrides)),
		Features: make(map[string]bool, len(p.Features)+len(e.FeatureOverrides)),
	}
	for k, v := range p.Quotas {
		out.Quotas[k] = v
	}
	for k, v := range e.QuotaOverrides {
		out.Quotas[k] = v
	}
	for k, v := range p.Features {
		out.Features[k] = v
	}
	for k, v := range e.FeatureOverrides {
		out.Features[k] = v
	}
	return out
}

// Limit returns the effective limit for key. The bool is false when the key is
// not governed by the plan at all (treated as unlimited / unconstrained).
func (p Plan) Limit(key string) (int64, bool) {
	v, ok := p.Quotas[key]
	return v, ok
}

// HasFeature reports whether feature is enabled in the (already effective) plan.
// Unknown features are disabled by default.
func (p Plan) HasFeature(feature string) bool {
	return p.Features[feature]
}

// Remaining computes how much headroom is left for a key given the effective
// limit and current usage. Unlimited (-1) and ungoverned keys return Unlimited.
// A negative result is clamped to 0 (over-provisioned owners read as "full").
func Remaining(limit, used int64) int64 {
	if limit == Unlimited {
		return Unlimited
	}
	r := limit - used
	if r < 0 {
		return 0
	}
	return r
}

// Allows reports whether `delta` more of a key fits under `limit` given `used`.
// Unlimited always allows. Non-positive deltas always allow (releases / no-ops).
func Allows(limit, used, delta int64) bool {
	if limit == Unlimited {
		return true
	}
	if delta <= 0 {
		return true
	}
	return used+delta <= limit
}
