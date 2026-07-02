package app

import (
	"context"
	"errors"
	"testing"
	"time"

	cacheadapter "github.com/restorna/platform/services/settings/internal/adapters/cache"
	"github.com/restorna/platform/services/settings/internal/domain"
	"github.com/restorna/platform/services/settings/internal/ports"
)

// ---- in-memory fakes for the ports ---------------------------------------

type fakeDefs struct{ m map[string]domain.Definition }

func newFakeDefs(ds ...domain.Definition) *fakeDefs {
	f := &fakeDefs{m: map[string]domain.Definition{}}
	for _, d := range ds {
		f.m[d.Key] = d
	}
	return f
}

func (f *fakeDefs) UpsertDefinitions(_ context.Context, ds []domain.Definition) (int, error) {
	for _, d := range ds {
		f.m[d.Key] = d
	}
	return len(ds), nil
}

func (f *fakeDefs) GetDefinition(_ context.Context, key string) (domain.Definition, error) {
	d, ok := f.m[key]
	if !ok {
		return domain.Definition{}, domain.ErrNotFound
	}
	return d, nil
}

func (f *fakeDefs) ListDefinitions(_ context.Context, ns string) ([]domain.Definition, error) {
	var out []domain.Definition
	for _, d := range f.m {
		if domain.InNamespace(d.Key, ns) {
			out = append(out, d)
		}
	}
	return out, nil
}

// fakeOvers mirrors the pg adapter's owner-scoped storage + transactional event
// staging in memory.
type fakeOvers struct {
	m      map[string]domain.Override // pk: key|owner|brand|restaurant
	staged []ports.OverrideEvent
}

func newFakeOvers() *fakeOvers { return &fakeOvers{m: map[string]domain.Override{}} }

func okey(o domain.Override) string {
	return o.Key + "|" + o.OwnerID + "|" + o.BrandID + "|" + o.RestaurantID
}

func (f *fakeOvers) SetOverride(_ context.Context, o domain.Override, evt ports.OverrideEvent) error {
	f.m[okey(o)] = o
	f.staged = append(f.staged, evt) // emulates outbox.Stage in the same tx
	return nil
}

func (f *fakeOvers) OverridesFor(_ context.Context, owner, brand, restaurant string, keys []string) ([]domain.Override, error) {
	want := map[string]bool{}
	for _, k := range keys {
		want[k] = true
	}
	var out []domain.Override
	for _, o := range f.m {
		if o.OwnerID != owner {
			continue
		}
		if len(want) > 0 && !want[o.Key] {
			continue
		}
		switch o.Scope {
		case domain.ScopeOwner:
			out = append(out, o)
		case domain.ScopeBrand:
			if o.BrandID == brand {
				out = append(out, o)
			}
		case domain.ScopeRestaurant:
			if o.RestaurantID == restaurant {
				out = append(out, o)
			}
		}
	}
	return out, nil
}

// roleCtxKey threads a role through context for the RoleFn under test.
type roleCtxKey struct{}

func withRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, roleCtxKey{}, role)
}

func roleFromCtx(ctx context.Context) (string, bool) {
	r, ok := ctx.Value(roleCtxKey{}).(string)
	return r, ok
}

// ---- fixtures -------------------------------------------------------------

func gstDef() domain.Definition {
	return domain.Definition{
		Key:        "billing.gst_pct",
		Type:       domain.TypeInt,
		Default:    domain.Value{Type: domain.TypeInt, Raw: "5"},
		MaxScope:   domain.ScopeRestaurant,
		Validation: "min:0,max:28",
		EditableBy: "owner",
	}
}

func currencyDef() domain.Definition {
	return domain.Definition{
		Key:        "billing.currency",
		Type:       domain.TypeString,
		Default:    domain.Value{Type: domain.TypeString, Raw: "INR"},
		MaxScope:   domain.ScopeOwner, // owner-only ceiling
		Validation: "min:3,max:3",
		EditableBy: "platform_admin",
	}
}

func roundingDef() domain.Definition {
	return domain.Definition{
		Key:         "billing.rounding",
		Type:        domain.TypeEnum,
		Default:     domain.Value{Type: domain.TypeEnum, Raw: "nearest_1"},
		MaxScope:    domain.ScopeRestaurant,
		EnumOptions: []string{"nearest_1", "none"},
		EditableBy:  "owner",
	}
}

func newSvc(cache ports.Cache, defs ...domain.Definition) (*Service, *fakeOvers) {
	if len(defs) == 0 {
		defs = []domain.Definition{gstDef(), currencyDef(), roundingDef()}
	}
	overs := newFakeOvers()
	return New(newFakeDefs(defs...), overs, cache, roleFromCtx), overs
}

// ---- tests ----------------------------------------------------------------

func TestSetOverrideHappyPath(t *testing.T) {
	svc, overs := newSvc(nil)
	ctx := withRole(context.Background(), "owner")

	sv, err := svc.SetOverride(ctx, SetOverrideInput{
		OwnerID: "own_1",
		Key:     "billing.gst_pct",
		Value:   domain.Value{Type: domain.TypeInt, Raw: "18"},
	})
	if err != nil {
		t.Fatalf("SetOverride: %v", err)
	}
	if sv.Value.Raw != "18" || sv.SourceScope != domain.ScopeOwner {
		t.Errorf("got %+v", sv)
	}
	if len(overs.staged) != 1 || overs.staged[0].Type != EventOverrideChanged {
		t.Errorf("expected one override.changed event staged, got %+v", overs.staged)
	}
}

func TestSetOverrideValidationRejectsBadValue(t *testing.T) {
	svc, _ := newSvc(nil)
	ctx := withRole(context.Background(), "owner")

	tests := []struct {
		name string
		key  string
		raw  string
	}{
		{"gst above max", "billing.gst_pct", "40"},
		{"gst not int", "billing.gst_pct", "ten"},
		{"rounding not in enum", "billing.rounding", "nearest_5"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.SetOverride(ctx, SetOverrideInput{
				OwnerID: "own_1", Key: tc.key,
				Value: domain.Value{Raw: tc.raw},
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("expected ErrInvalid, got %v", err)
			}
		})
	}
}

func TestSetOverrideEditableByEnforced(t *testing.T) {
	svc, _ := newSvc(nil)

	// manager may NOT edit an owner-editable setting.
	_, err := svc.SetOverride(withRole(context.Background(), "manager"), SetOverrideInput{
		OwnerID: "own_1", Key: "billing.gst_pct",
		Value: domain.Value{Type: domain.TypeInt, Raw: "12"},
	})
	if !errors.Is(err, domain.ErrNotEditable) {
		t.Fatalf("manager should be denied, got %v", err)
	}

	// platform_admin (senior) MAY edit the same setting.
	if _, err := svc.SetOverride(withRole(context.Background(), "platform_admin"), SetOverrideInput{
		OwnerID: "own_1", Key: "billing.gst_pct",
		Value: domain.Value{Type: domain.TypeInt, Raw: "12"},
	}); err != nil {
		t.Fatalf("platform_admin should be allowed, got %v", err)
	}
}

func TestSetOverrideScopeTooDeep(t *testing.T) {
	svc, _ := newSvc(nil)
	ctx := withRole(context.Background(), "platform_admin")

	// billing.currency is owner-only; targeting a brand scope must be rejected.
	_, err := svc.SetOverride(ctx, SetOverrideInput{
		OwnerID: "own_1", BrandID: "brnd_1", Key: "billing.currency",
		Value: domain.Value{Type: domain.TypeString, Raw: "USD"},
	})
	if !errors.Is(err, domain.ErrScopeTooDeep) {
		t.Fatalf("expected ErrScopeTooDeep, got %v", err)
	}
}

func TestSetOverrideUnknownKey(t *testing.T) {
	svc, _ := newSvc(nil)
	ctx := withRole(context.Background(), "owner")
	_, err := svc.SetOverride(ctx, SetOverrideInput{
		OwnerID: "own_1", Key: "nope.nope", Value: domain.Value{Raw: "1"},
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetEffectiveDefaultFallback(t *testing.T) {
	svc, _ := newSvc(nil)
	ctx := context.Background()

	vals, err := svc.GetEffective(ctx, GetEffectiveInput{
		OwnerID: "own_1", Keys: []string{"billing.gst_pct"},
	})
	if err != nil {
		t.Fatalf("GetEffective: %v", err)
	}
	if len(vals) != 1 || vals[0].Value.Raw != "5" || vals[0].SourceScope != domain.ScopeDefinition {
		t.Fatalf("expected default 5 from definition, got %+v", vals)
	}
}

func TestGetEffectivePrecedenceEndToEnd(t *testing.T) {
	svc, _ := newSvc(nil)
	admin := withRole(context.Background(), "owner")

	// owner-level 18, then restaurant-level 12 for out_1.
	mustSet(t, svc, admin, SetOverrideInput{OwnerID: "own_1", Key: "billing.gst_pct", Value: domain.Value{Type: domain.TypeInt, Raw: "18"}})
	mustSet(t, svc, admin, SetOverrideInput{OwnerID: "own_1", RestaurantID: "out_1", Key: "billing.gst_pct", Value: domain.Value{Type: domain.TypeInt, Raw: "12"}})

	// Resolving for the restaurant sees 12 (restaurant wins).
	got := effective(t, svc, GetEffectiveInput{OwnerID: "own_1", RestaurantID: "out_1", Keys: []string{"billing.gst_pct"}})
	if got["billing.gst_pct"].Value.Raw != "12" || got["billing.gst_pct"].SourceScope != domain.ScopeRestaurant {
		t.Errorf("restaurant should win: %+v", got["billing.gst_pct"])
	}

	// Resolving for a DIFFERENT restaurant falls back to the owner-level 18.
	got2 := effective(t, svc, GetEffectiveInput{OwnerID: "own_1", RestaurantID: "out_2", Keys: []string{"billing.gst_pct"}})
	if got2["billing.gst_pct"].Value.Raw != "18" || got2["billing.gst_pct"].SourceScope != domain.ScopeOwner {
		t.Errorf("other outlet should fall back to owner: %+v", got2["billing.gst_pct"])
	}
}

func TestCacheServesAndInvalidatesOnSet(t *testing.T) {
	cache := cacheadapter.New(time.Minute)
	svc, overs := newSvc(cache)
	admin := withRole(context.Background(), "owner")

	// First read populates the cache from the default.
	got := effective(t, svc, GetEffectiveInput{OwnerID: "own_1", Keys: []string{"billing.gst_pct"}})
	if got["billing.gst_pct"].Value.Raw != "5" {
		t.Fatalf("want default 5, got %+v", got["billing.gst_pct"])
	}

	// Mutate the underlying store directly WITHOUT going through SetOverride: a
	// still-valid cache entry should keep serving the old value (proves caching).
	overs.m[okey(domain.Override{Key: "billing.gst_pct", OwnerID: "own_1", Scope: domain.ScopeOwner})] =
		domain.Override{Key: "billing.gst_pct", OwnerID: "own_1", Scope: domain.ScopeOwner, Value: domain.Value{Type: domain.TypeInt, Raw: "9"}}

	gotCached := effective(t, svc, GetEffectiveInput{OwnerID: "own_1", Keys: []string{"billing.gst_pct"}})
	if gotCached["billing.gst_pct"].Value.Raw != "5" {
		t.Fatalf("cache should still serve 5, got %+v", gotCached["billing.gst_pct"])
	}

	// A real SetOverride invalidates the owner's cache; the next read sees 22.
	mustSet(t, svc, admin, SetOverrideInput{OwnerID: "own_1", Key: "billing.gst_pct", Value: domain.Value{Type: domain.TypeInt, Raw: "22"}})
	gotFresh := effective(t, svc, GetEffectiveInput{OwnerID: "own_1", Keys: []string{"billing.gst_pct"}})
	if gotFresh["billing.gst_pct"].Value.Raw != "22" {
		t.Fatalf("post-invalidate read should be 22, got %+v", gotFresh["billing.gst_pct"])
	}
}

func TestRegisterDefinitionsValidates(t *testing.T) {
	svc, _ := newSvc(nil)
	ctx := context.Background()

	// Valid batch upserts and returns the count.
	n, err := svc.RegisterDefinitions(ctx, []domain.Definition{gstDef(), roundingDef()})
	if err != nil || n != 2 {
		t.Fatalf("RegisterDefinitions: n=%d err=%v", n, err)
	}

	// A bad definition (default out of range) fails the whole batch.
	bad := gstDef()
	bad.Default = domain.Value{Type: domain.TypeInt, Raw: "99"} // > max 28
	if _, err := svc.RegisterDefinitions(ctx, []domain.Definition{bad}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("expected ErrInvalid for bad default, got %v", err)
	}
}

// ---- helpers --------------------------------------------------------------

func mustSet(t *testing.T, svc *Service, ctx context.Context, in SetOverrideInput) {
	t.Helper()
	if _, err := svc.SetOverride(ctx, in); err != nil {
		t.Fatalf("SetOverride(%s): %v", in.Key, err)
	}
}

func effective(t *testing.T, svc *Service, in GetEffectiveInput) map[string]domain.SettingValue {
	t.Helper()
	vals, err := svc.GetEffective(context.Background(), in)
	if err != nil {
		t.Fatalf("GetEffective: %v", err)
	}
	out := map[string]domain.SettingValue{}
	for _, v := range vals {
		out[v.Key] = v
	}
	return out
}
