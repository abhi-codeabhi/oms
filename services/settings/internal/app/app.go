// Package app holds the settings use cases: register definitions, list them, set
// an override (enforcing max_scope + editable_by + validation), and resolve the
// effective value by precedence. It depends only on ports + domain — never
// pgx/connect/nats.
//
// GetEffective is the hot path and is fronted by an in-process TTL cache that is
// invalidated (per-owner) on every SetOverride. Other services keep their OWN
// caches and invalidate them by subscribing to restorna.settings.override.changed.v1.
package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/restorna/platform/services/settings/internal/domain"
	"github.com/restorna/platform/services/settings/internal/ports"
)

// EventOverrideChanged is emitted (via the outbox) whenever an override is set;
// consumers invalidate their local caches on it.
const EventOverrideChanged = "restorna.settings.override.changed.v1"

// RoleFn extracts the caller's role name ("owner", "manager", "platform_admin",
// …) from context. It is injected so the app stays free of pkg/tenancy import
// rules and tests can drive it directly. It returns ("", false) when no scope is
// set.
type RoleFn func(ctx context.Context) (role string, ok bool)

// Service implements the settings use cases over its repositories + cache.
type Service struct {
	defs  ports.DefinitionRepo
	overs ports.OverrideRepo
	cache ports.Cache
	role  RoleFn
}

// New wires the service. cache may be nil (caching disabled). role may be nil, in
// which case editable_by is enforced as platform_admin-only (fail closed).
func New(defs ports.DefinitionRepo, overs ports.OverrideRepo, cache ports.Cache, role RoleFn) *Service {
	if role == nil {
		role = func(context.Context) (string, bool) { return "", false }
	}
	return &Service{defs: defs, overs: overs, cache: cache, role: role}
}

// RegisterDefinitions idempotently upserts a batch of definitions (services call
// this on boot). Each is validated; a bad definition fails the whole batch.
func (s *Service) RegisterDefinitions(ctx context.Context, defs []domain.Definition) (int, error) {
	if len(defs) == 0 {
		return 0, nil
	}
	for _, d := range defs {
		if err := d.Validate(); err != nil {
			return 0, err
		}
	}
	return s.defs.UpsertDefinitions(ctx, defs)
}

// ListDefinitions returns the definition catalog filtered by namespace ("" = all).
func (s *Service) ListDefinitions(ctx context.Context, namespace string) ([]domain.Definition, error) {
	return s.defs.ListDefinitions(ctx, namespace)
}

// SetOverrideInput is the validated input for setting an override.
type SetOverrideInput struct {
	OwnerID      string
	BrandID      string
	RestaurantID string
	Key          string
	Value        domain.Value
}

// SetOverride stores a value at the owner/brand/restaurant scope implied by the
// request, after enforcing:
//   - the key has a definition (NotFound otherwise),
//   - the target scope is not deeper than the definition's max_scope,
//   - the caller's role may edit the key (editable_by),
//   - the value passes the definition's type + validation rules.
//
// It then persists the override + stages the change event in one tx and
// invalidates the owner's cache entries.
func (s *Service) SetOverride(ctx context.Context, in SetOverrideInput) (domain.SettingValue, error) {
	if strings.TrimSpace(in.Key) == "" {
		return domain.SettingValue{}, fmt.Errorf("%w: key is required", domain.ErrInvalid)
	}
	if strings.TrimSpace(in.OwnerID) == "" {
		return domain.SettingValue{}, fmt.Errorf("%w: owner is required", domain.ErrInvalid)
	}

	def, err := s.defs.GetDefinition(ctx, in.Key)
	if err != nil {
		return domain.SettingValue{}, err
	}

	scope := scopeOf(in.OwnerID, in.BrandID, in.RestaurantID)
	if !def.AllowsScope(scope) {
		return domain.SettingValue{}, fmt.Errorf("%w: %q allows %v at most, got %v",
			domain.ErrScopeTooDeep, in.Key, def.MaxScope, scope)
	}

	// editable_by enforcement against the caller's role from tenancy context.
	role, _ := s.role(ctx)
	if !def.EditableByRole(role) {
		return domain.SettingValue{}, fmt.Errorf("%w: %q requires %q, caller is %q",
			domain.ErrNotEditable, in.Key, def.EditableBy, role)
	}

	// Normalise the value's type to the definition, then validate.
	v := in.Value
	if v.Type == domain.TypeUnspecified {
		v.Type = def.Type
	}
	if err := def.Check(v); err != nil {
		return domain.SettingValue{}, err
	}

	o := domain.Override{
		Key:          in.Key,
		OwnerID:      in.OwnerID,
		BrandID:      in.BrandID,
		RestaurantID: in.RestaurantID,
		Scope:        scope,
		Value:        v,
	}

	evt := ports.OverrideEvent{
		Type:     EventOverrideChanged,
		TenantID: in.OwnerID,
		Data: EventOverrideChangedPayload{
			Key:          in.Key,
			OwnerID:      in.OwnerID,
			BrandID:      in.BrandID,
			RestaurantID: in.RestaurantID,
			Scope:        ScopeName(scope),
			Raw:          v.Raw,
		},
	}
	if err := s.overs.SetOverride(ctx, o, evt); err != nil {
		return domain.SettingValue{}, err
	}

	// Cache invalidation: any of this owner's effective values may have changed.
	if s.cache != nil {
		s.cache.InvalidateOwner(in.OwnerID)
	}

	return domain.SettingValue{Key: in.Key, Value: v, SourceScope: scope}, nil
}

// GetEffectiveInput is the validated input for resolving effective values.
type GetEffectiveInput struct {
	OwnerID      string
	BrandID      string
	RestaurantID string
	Keys         []string // empty = all keys in Namespace
	Namespace    string
}

// GetEffective resolves the effective value for each requested key by precedence
// restaurant > brand > owner > definition default. It consults the in-process TTL
// cache per (scope,key) first; misses are resolved from the repos and cached.
func (s *Service) GetEffective(ctx context.Context, in GetEffectiveInput) ([]domain.SettingValue, error) {
	if strings.TrimSpace(in.OwnerID) == "" {
		return nil, fmt.Errorf("%w: owner is required", domain.ErrInvalid)
	}

	// Determine the key set to resolve.
	defs, err := s.defs.ListDefinitions(ctx, in.Namespace)
	if err != nil {
		return nil, err
	}
	defByKey := make(map[string]domain.Definition, len(defs))
	for _, d := range defs {
		defByKey[d.Key] = d
	}

	keys := in.Keys
	if len(keys) == 0 {
		for _, d := range defs {
			keys = append(keys, d.Key)
		}
	}

	// Cache fast path: collect hits, leaving the rest to resolve.
	out := make([]domain.SettingValue, 0, len(keys))
	var miss []string
	for _, k := range keys {
		if s.cache != nil {
			if v, ok := s.cache.Get(s.cacheKey(in, k)); ok {
				out = append(out, v)
				continue
			}
		}
		miss = append(miss, k)
	}

	if len(miss) > 0 {
		overrides, err := s.overs.OverridesFor(ctx, in.OwnerID, in.BrandID, in.RestaurantID, miss)
		if err != nil {
			return nil, err
		}
		for _, k := range miss {
			def, ok := defByKey[k]
			if !ok {
				// Resolve the definition individually for keys outside the listed
				// namespace (e.g. explicit keys with no namespace filter).
				def, err = s.defs.GetDefinition(ctx, k)
				if err != nil {
					return nil, err
				}
			}
			sv := domain.Resolve(def, overrides)
			if s.cache != nil {
				s.cache.Set(s.cacheKey(in, k), sv)
			}
			out = append(out, sv)
		}
	}

	return out, nil
}

// EventOverrideChangedPayload is the JSON shape of the override.changed event.
type EventOverrideChangedPayload struct {
	Key          string `json:"key"`
	OwnerID      string `json:"owner_id"`
	BrandID      string `json:"brand_id,omitempty"`
	RestaurantID string `json:"restaurant_id,omitempty"`
	Scope        string `json:"scope"`
	Raw          string `json:"raw"`
}

// ScopeName renders a domain.Scope as the lowercase token used in events.
func ScopeName(s domain.Scope) string {
	switch s {
	case domain.ScopeOwner:
		return "owner"
	case domain.ScopeBrand:
		return "brand"
	case domain.ScopeRestaurant:
		return "restaurant"
	case domain.ScopeDefinition:
		return "definition"
	default:
		return "unspecified"
	}
}

// cacheKey builds the per-scope cache key for a setting key.
func (s *Service) cacheKey(in GetEffectiveInput, key string) string {
	return in.OwnerID + "|" + in.BrandID + "|" + in.RestaurantID + "|" + key
}

// scopeOf derives the override scope from which tenant ids are present. The
// deepest non-empty id wins: restaurant > brand > owner.
func scopeOf(ownerID, brandID, restaurantID string) domain.Scope {
	switch {
	case restaurantID != "":
		return domain.ScopeRestaurant
	case brandID != "":
		return domain.ScopeBrand
	case ownerID != "":
		return domain.ScopeOwner
	default:
		return domain.ScopeUnspecified
	}
}
