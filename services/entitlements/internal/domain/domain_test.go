package domain

import (
	"reflect"
	"testing"
)

func TestEffectivePlan_MergeAndOverride(t *testing.T) {
	plan := Plan{
		ID:   "growth",
		Name: "Growth",
		Quotas: map[string]int64{
			"outlets":      3,
			"staff.waiter": 15,
			"tables":       40,
		},
		Features: map[string]bool{
			"aggregators":   true,
			"analytics_pro": false,
			"crm":           false,
		},
	}
	ent := Entitlement{
		OwnerID: "own_1",
		PlanID:  "growth",
		QuotaOverrides: map[string]int64{
			"staff.waiter": 25, // bump
			"connectors":   5,  // new key only in override
		},
		FeatureOverrides: map[string]bool{
			"analytics_pro": true, // flip on
		},
	}

	got := EffectivePlan(plan, ent)

	wantQuotas := map[string]int64{
		"outlets":      3,
		"staff.waiter": 25, // overridden
		"tables":       40,
		"connectors":   5, // added
	}
	if !reflect.DeepEqual(got.Quotas, wantQuotas) {
		t.Errorf("quotas = %v, want %v", got.Quotas, wantQuotas)
	}
	wantFeatures := map[string]bool{
		"aggregators":   true,
		"analytics_pro": true, // overridden on
		"crm":           false,
	}
	if !reflect.DeepEqual(got.Features, wantFeatures) {
		t.Errorf("features = %v, want %v", got.Features, wantFeatures)
	}
	if got.ID != "growth" || got.Name != "Growth" {
		t.Errorf("plan identity not preserved: %+v", got)
	}
	// original plan must not be mutated
	if plan.Quotas["staff.waiter"] != 15 {
		t.Errorf("source plan was mutated: %v", plan.Quotas)
	}
}

func TestRemaining(t *testing.T) {
	tests := []struct {
		name        string
		limit, used int64
		want        int64
	}{
		{"headroom", 10, 3, 7},
		{"exact", 10, 10, 0},
		{"over clamps to 0", 10, 14, 0},
		{"unlimited", Unlimited, 9999, Unlimited},
		{"zero limit", 0, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Remaining(tc.limit, tc.used); got != tc.want {
				t.Errorf("Remaining(%d,%d) = %d, want %d", tc.limit, tc.used, got, tc.want)
			}
		})
	}
}

func TestAllows(t *testing.T) {
	tests := []struct {
		name               string
		limit, used, delta int64
		want               bool
	}{
		{"fits", 10, 5, 3, true},
		{"exact fit", 10, 7, 3, true},
		{"over by one", 10, 8, 3, false},
		{"already full", 10, 10, 1, false},
		{"unlimited", Unlimited, 1_000_000, 50, true},
		{"release always ok", 5, 5, -2, true},
		{"zero delta noop", 5, 5, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allows(tc.limit, tc.used, tc.delta); got != tc.want {
				t.Errorf("Allows(%d,%d,%d) = %v, want %v", tc.limit, tc.used, tc.delta, got, tc.want)
			}
		})
	}
}

func TestHasFeatureDefaultsFalse(t *testing.T) {
	p := Plan{Features: map[string]bool{"crm": true}}
	if !p.HasFeature("crm") {
		t.Error("crm should be enabled")
	}
	if p.HasFeature("unknown") {
		t.Error("unknown feature should default false")
	}
}

func TestValidate(t *testing.T) {
	if (Entitlement{PlanID: "p"}).Validate() == nil {
		t.Error("empty owner should be invalid")
	}
	if (Entitlement{OwnerID: "o"}).Validate() == nil {
		t.Error("empty plan should be invalid")
	}
	if (Entitlement{OwnerID: "o", PlanID: "p"}).Validate() != nil {
		t.Error("valid entitlement rejected")
	}
	if (Plan{}).Validate() == nil {
		t.Error("empty plan id should be invalid")
	}
}
