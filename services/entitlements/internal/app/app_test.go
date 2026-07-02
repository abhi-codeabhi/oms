package app

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/restorna/platform/services/entitlements/internal/domain"
)

// ---- in-memory fakes for the ports ---------------------------------------

type fakePlans struct{ m map[string]domain.Plan }

func newFakePlans(ps ...domain.Plan) *fakePlans {
	f := &fakePlans{m: map[string]domain.Plan{}}
	for _, p := range ps {
		f.m[p.ID] = p
	}
	return f
}
func (f *fakePlans) GetPlan(_ context.Context, id string) (domain.Plan, error) {
	p, ok := f.m[id]
	if !ok {
		return domain.Plan{}, domain.ErrPlanNotFound
	}
	return p, nil
}
func (f *fakePlans) UpsertPlan(_ context.Context, p domain.Plan) (domain.Plan, error) {
	f.m[p.ID] = p
	return p, nil
}
func (f *fakePlans) ListPlans(_ context.Context) ([]domain.Plan, error) {
	out := make([]domain.Plan, 0, len(f.m))
	for _, p := range f.m {
		out = append(out, p)
	}
	return out, nil
}

type fakeEnts struct{ m map[string]domain.Entitlement }

func newFakeEnts(es ...domain.Entitlement) *fakeEnts {
	f := &fakeEnts{m: map[string]domain.Entitlement{}}
	for _, e := range es {
		f.m[e.OwnerID] = e
	}
	return f
}
func (f *fakeEnts) GetEntitlement(_ context.Context, owner string) (domain.Entitlement, error) {
	e, ok := f.m[owner]
	if !ok {
		return domain.Entitlement{}, domain.ErrEntitlementNotFound
	}
	return e, nil
}
func (f *fakeEnts) UpsertEntitlement(_ context.Context, e domain.Entitlement) (domain.Entitlement, error) {
	f.m[e.OwnerID] = e
	return e, nil
}

// fakeUsage mirrors the pg adapter's atomic + idempotent semantics in memory.
type fakeUsage struct {
	mu       sync.Mutex
	counters map[string]int64  // owner|key -> used
	ledger   map[string]ledger // reservation_id -> {owner,key,delta}
}
type ledger struct {
	owner, key string
	delta      int64
}

func newFakeUsage() *fakeUsage {
	return &fakeUsage{counters: map[string]int64{}, ledger: map[string]ledger{}}
}
func ck(owner, key string) string { return owner + "|" + key }

func (f *fakeUsage) Used(_ context.Context, owner, key string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.counters[ck(owner, key)], nil
}
func (f *fakeUsage) Reserve(_ context.Context, owner, key string, delta, limit int64, rid string) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	used := f.counters[ck(owner, key)]
	if _, dup := f.ledger[rid]; dup {
		// idempotent replay: no re-count
		return domain.Remaining(limit, used), nil
	}
	if !domain.Allows(limit, used, delta) {
		return 0, domain.ErrQuotaExceeded
	}
	f.ledger[rid] = ledger{owner, key, delta}
	used += delta
	f.counters[ck(owner, key)] = used
	return domain.Remaining(limit, used), nil
}
func (f *fakeUsage) Release(_ context.Context, owner, key string, _ int64, rid string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.ledger[rid]
	if !ok {
		return nil // idempotent no-op
	}
	delete(f.ledger, rid)
	u := f.counters[ck(l.owner, l.key)] - l.delta
	if u < 0 {
		u = 0
	}
	f.counters[ck(l.owner, l.key)] = u
	return nil
}

// ---- fixtures -------------------------------------------------------------

func growthPlan() domain.Plan {
	return domain.Plan{
		ID:   "growth",
		Name: "Growth",
		Quotas: map[string]int64{
			"staff.waiter": 5,
			"outlets":      3,
		},
		Features: map[string]bool{
			"aggregators": true,
			"crm":         false,
		},
	}
}

func newSvc(ent domain.Entitlement) (*Service, *fakeUsage) {
	usage := newFakeUsage()
	return New(newFakePlans(growthPlan()), newFakeEnts(ent), usage), usage
}

// ---- tests ----------------------------------------------------------------

func TestCheckQuota(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name          string
		overrides     map[string]int64
		key           string
		delta         int64
		preload       int64 // seed used
		wantAllowed   bool
		wantRemaining int64
		wantLimit     int64
	}{
		{"under limit", nil, "staff.waiter", 1, 0, true, 5, 5},
		{"at boundary", nil, "staff.waiter", 5, 0, true, 5, 5},
		{"over limit", nil, "staff.waiter", 6, 0, false, 5, 5},
		{"with usage leaves headroom", nil, "staff.waiter", 1, 4, true, 1, 5},
		{"with usage over", nil, "staff.waiter", 2, 4, false, 1, 5},
		{"override raises limit", map[string]int64{"staff.waiter": 10}, "staff.waiter", 8, 0, true, 10, 10},
		{"ungoverned key unlimited", nil, "tables", 999, 0, true, domain.Unlimited, domain.Unlimited},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, usage := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth", QuotaOverrides: tc.overrides})
			if tc.preload > 0 {
				usage.counters[ck("own_1", tc.key)] = tc.preload
			}
			res, err := svc.CheckQuota(ctx, "own_1", tc.key, tc.delta)
			if err != nil {
				t.Fatalf("CheckQuota: %v", err)
			}
			if res.Allowed != tc.wantAllowed {
				t.Errorf("allowed = %v, want %v", res.Allowed, tc.wantAllowed)
			}
			if res.Remaining != tc.wantRemaining {
				t.Errorf("remaining = %d, want %d", res.Remaining, tc.wantRemaining)
			}
			if res.Limit != tc.wantLimit {
				t.Errorf("limit = %d, want %d", res.Limit, tc.wantLimit)
			}
			if !tc.wantAllowed && res.UpgradeHint == "" {
				t.Error("expected an upgrade hint on denial")
			}
		})
	}
}

func TestReserveDecrementsRemaining(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})

	ok, rem, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 2, "rsv_a")
	if err != nil || !ok {
		t.Fatalf("first reserve: ok=%v err=%v", ok, err)
	}
	if rem != 3 { // 5 - 2
		t.Fatalf("remaining after first reserve = %d, want 3", rem)
	}
	ok, rem, err = svc.ReserveQuota(ctx, "own_1", "staff.waiter", 1, "rsv_b")
	if err != nil || !ok {
		t.Fatalf("second reserve: ok=%v err=%v", ok, err)
	}
	if rem != 2 { // 5 - 3
		t.Fatalf("remaining after second reserve = %d, want 2", rem)
	}
}

func TestReserveIsIdempotentByReservationID(t *testing.T) {
	ctx := context.Background()
	svc, usage := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})

	for i := 0; i < 3; i++ {
		ok, rem, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 2, "rsv_same")
		if err != nil || !ok {
			t.Fatalf("reserve %d: ok=%v err=%v", i, ok, err)
		}
		if rem != 3 {
			t.Fatalf("reserve %d remaining = %d, want 3 (no double count)", i, rem)
		}
	}
	if got := usage.counters[ck("own_1", "staff.waiter")]; got != 2 {
		t.Fatalf("counter = %d, want 2 (counted once despite 3 calls)", got)
	}
}

func TestReserveOverLimitReturnsQuotaExceeded(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})

	if _, _, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 5, "rsv_fill"); err != nil {
		t.Fatalf("fill to limit: %v", err)
	}
	_, _, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 1, "rsv_over")
	if !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded, got %v", err)
	}
}

func TestReleaseRestoresHeadroom(t *testing.T) {
	ctx := context.Background()
	svc, usage := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})

	if _, _, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 3, "rsv_x"); err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if ok, err := svc.ReleaseQuota(ctx, "own_1", "staff.waiter", 3, "rsv_x"); err != nil || !ok {
		t.Fatalf("release: ok=%v err=%v", ok, err)
	}
	if got := usage.counters[ck("own_1", "staff.waiter")]; got != 0 {
		t.Fatalf("counter after release = %d, want 0", got)
	}
	// Released id is gone: re-release is a harmless no-op.
	if ok, err := svc.ReleaseQuota(ctx, "own_1", "staff.waiter", 3, "rsv_x"); err != nil || !ok {
		t.Fatalf("re-release should be no-op: ok=%v err=%v", ok, err)
	}
	// And the full limit is reservable again under a fresh id.
	if _, rem, err := svc.ReserveQuota(ctx, "own_1", "staff.waiter", 5, "rsv_y"); err != nil || rem != 0 {
		t.Fatalf("reserve full after release: rem=%d err=%v", rem, err)
	}
}

func TestHasFeatureWithOverride(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name     string
		feature  string
		fOver    map[string]bool
		want     bool
	}{
		{"plan default on", "aggregators", nil, true},
		{"plan default off", "crm", nil, false},
		{"override turns on", "crm", map[string]bool{"crm": true}, true},
		{"override turns off", "aggregators", map[string]bool{"aggregators": false}, false},
		{"unknown feature", "analytics_pro", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth", FeatureOverrides: tc.fOver})
			got, err := svc.HasFeature(ctx, "own_1", tc.feature)
			if err != nil {
				t.Fatalf("HasFeature: %v", err)
			}
			if got != tc.want {
				t.Errorf("HasFeature(%q) = %v, want %v", tc.feature, got, tc.want)
			}
		})
	}
}

func TestGetEntitlementMergesEffectivePlan(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(domain.Entitlement{
		OwnerID:        "own_1",
		PlanID:         "growth",
		QuotaOverrides: map[string]int64{"staff.waiter": 12},
	})
	ent, plan, err := svc.GetEntitlement(ctx, "own_1")
	if err != nil {
		t.Fatalf("GetEntitlement: %v", err)
	}
	if ent.PlanID != "growth" {
		t.Errorf("plan id = %q", ent.PlanID)
	}
	if plan.Quotas["staff.waiter"] != 12 {
		t.Errorf("effective staff.waiter = %d, want 12 (override applied)", plan.Quotas["staff.waiter"])
	}
	if plan.Quotas["outlets"] != 3 {
		t.Errorf("effective outlets = %d, want 3 (plan default kept)", plan.Quotas["outlets"])
	}
}

func TestSetEntitlementRejectsUnknownPlan(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})
	_, err := svc.SetEntitlement(ctx, domain.Entitlement{OwnerID: "own_2", PlanID: "nope"})
	if !errors.Is(err, domain.ErrPlanNotFound) {
		t.Fatalf("expected ErrPlanNotFound, got %v", err)
	}
}

func TestListPlansReturnsCatalog(t *testing.T) {
	ctx := context.Background()
	free := domain.Plan{ID: "free", Name: "Free"}
	svc := New(newFakePlans(growthPlan(), free), newFakeEnts(), newFakeUsage())

	plans, err := svc.ListPlans(ctx)
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(plans) != 2 {
		t.Fatalf("want 2 plans, got %d", len(plans))
	}
	ids := map[string]bool{}
	for _, p := range plans {
		ids[p.ID] = true
	}
	if !ids["growth"] || !ids["free"] {
		t.Fatalf("missing expected plans: %+v", ids)
	}
}

func TestInvalidArguments(t *testing.T) {
	ctx := context.Background()
	svc, _ := newSvc(domain.Entitlement{OwnerID: "own_1", PlanID: "growth"})
	if _, err := svc.CheckQuota(ctx, "own_1", "", 1); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty key: %v", err)
	}
	if _, _, err := svc.ReserveQuota(ctx, "own_1", "k", 1, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty reservation id: %v", err)
	}
	if _, err := svc.HasFeature(ctx, "", "f"); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("empty owner: %v", err)
	}
}
