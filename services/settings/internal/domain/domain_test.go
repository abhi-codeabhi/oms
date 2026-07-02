package domain

import (
	"errors"
	"testing"
)

func intDef(key string, def int, validation string) Definition {
	return Definition{
		Key:        key,
		Type:       TypeInt,
		Default:    Value{Type: TypeInt, Raw: itoa(def)},
		MaxScope:   ScopeRestaurant,
		Validation: validation,
		EditableBy: "owner",
	}
}

func itoa(n int) string {
	// tiny helper to avoid importing strconv in the test fixtures
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

func TestDefinitionCheck(t *testing.T) {
	enumDef := Definition{
		Key:         "billing.rounding",
		Type:        TypeEnum,
		Default:     Value{Type: TypeEnum, Raw: "nearest_1"},
		MaxScope:    ScopeRestaurant,
		EnumOptions: []string{"nearest_1", "none"},
		EditableBy:  "owner",
	}
	strDef := Definition{
		Key:        "billing.currency",
		Type:       TypeString,
		Default:    Value{Type: TypeString, Raw: "INR"},
		MaxScope:   ScopeOwner,
		Validation: "min:3,max:3",
		EditableBy: "platform_admin",
	}
	boolDef := Definition{
		Key:        "ordering.require_prepay",
		Type:       TypeBool,
		Default:    Value{Type: TypeBool, Raw: "false"},
		MaxScope:   ScopeRestaurant,
		EditableBy: "owner",
	}

	tests := []struct {
		name    string
		def     Definition
		raw     string
		wantErr bool
	}{
		{"int in range", intDef("billing.gst_pct", 5, "min:0,max:28"), "5", false},
		{"int below min", intDef("billing.gst_pct", 5, "min:0,max:28"), "-1", true},
		{"int above max", intDef("billing.gst_pct", 5, "min:0,max:28"), "29", true},
		{"int boundary max ok", intDef("billing.gst_pct", 5, "min:0,max:28"), "28", false},
		{"int not a number", intDef("billing.gst_pct", 5, ""), "abc", true},
		{"enum member", enumDef, "none", false},
		{"enum non-member", enumDef, "nearest_5", true},
		{"string exact len", strDef, "INR", false},
		{"string too short", strDef, "IN", true},
		{"string too long", strDef, "INRR", true},
		{"bool true", boolDef, "true", false},
		{"bool garbage", boolDef, "maybe", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.def.Check(Value{Type: tc.def.Type, Raw: tc.raw})
			if tc.wantErr && err == nil {
				t.Fatalf("expected error for %q", tc.raw)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.raw, err)
			}
			if err != nil && !errors.Is(err, ErrInvalid) {
				t.Errorf("error should wrap ErrInvalid, got %v", err)
			}
		})
	}
}

func TestDefinitionValidateRejectsBadDefault(t *testing.T) {
	d := Definition{
		Key:        "billing.gst_pct",
		Type:       TypeInt,
		Default:    Value{Type: TypeInt, Raw: "200"}, // breaks max:100
		MaxScope:   ScopeRestaurant,
		Validation: "min:0,max:100",
		EditableBy: "owner",
	}
	if err := d.Validate(); err == nil {
		t.Fatal("expected invalid default to fail Validate")
	}
	// enum without options
	e := Definition{Key: "x.y", Type: TypeEnum, MaxScope: ScopeOwner, Default: Value{Type: TypeEnum, Raw: "a"}}
	if err := e.Validate(); err == nil {
		t.Fatal("expected enum without options to fail Validate")
	}
}

func TestAllowsScope(t *testing.T) {
	ownerOnly := Definition{Key: "billing.currency", Type: TypeString, MaxScope: ScopeOwner, Default: Value{Type: TypeString, Raw: "INR"}}
	tests := []struct {
		name string
		def  Definition
		s    Scope
		want bool
	}{
		{"owner setting at owner ok", ownerOnly, ScopeOwner, true},
		{"owner setting at brand too deep", ownerOnly, ScopeBrand, false},
		{"owner setting at restaurant too deep", ownerOnly, ScopeRestaurant, false},
		{"restaurant setting at restaurant ok", intDef("k", 1, ""), ScopeRestaurant, true},
		{"restaurant setting at owner ok", intDef("k", 1, ""), ScopeOwner, true},
		{"unspecified never", intDef("k", 1, ""), ScopeUnspecified, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.def.AllowsScope(tc.s); got != tc.want {
				t.Errorf("AllowsScope(%v) = %v, want %v", tc.s, got, tc.want)
			}
		})
	}
}

func TestEditableByRole(t *testing.T) {
	mgr := Definition{EditableBy: "manager"}
	own := Definition{EditableBy: "owner"}
	adm := Definition{EditableBy: "platform_admin"}
	tests := []struct {
		name string
		def  Definition
		role string
		want bool
	}{
		{"manager edits manager setting", mgr, "manager", true},
		{"owner edits manager setting (senior)", mgr, "owner", true},
		{"admin edits manager setting (senior)", mgr, "platform_admin", true},
		{"manager cannot edit owner setting", own, "manager", false},
		{"owner edits owner setting", own, "owner", true},
		{"owner cannot edit admin setting", adm, "owner", false},
		{"admin edits admin setting", adm, "platform_admin", true},
		{"empty role denied", own, "", false},
		{"unknown editable_by is admin-only", Definition{EditableBy: "wizard"}, "owner", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.def.EditableByRole(tc.role); got != tc.want {
				t.Errorf("EditableByRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

func TestResolvePrecedence(t *testing.T) {
	def := intDef("floor.nudge.greet_secs", 30, "min:0,max:600")

	owner := Override{Key: def.Key, OwnerID: "own_1", Scope: ScopeOwner, Value: Value{Type: TypeInt, Raw: "25"}}
	brand := Override{Key: def.Key, OwnerID: "own_1", BrandID: "brnd_1", Scope: ScopeBrand, Value: Value{Type: TypeInt, Raw: "20"}}
	rest := Override{Key: def.Key, OwnerID: "own_1", RestaurantID: "out_1", Scope: ScopeRestaurant, Value: Value{Type: TypeInt, Raw: "15"}}

	tests := []struct {
		name      string
		overrides []Override
		wantRaw   string
		wantScope Scope
	}{
		{"no overrides -> default", nil, "30", ScopeDefinition},
		{"owner only", []Override{owner}, "25", ScopeOwner},
		{"brand beats owner", []Override{owner, brand}, "20", ScopeBrand},
		{"restaurant beats all", []Override{owner, brand, rest}, "15", ScopeRestaurant},
		{"restaurant alone", []Override{rest}, "15", ScopeRestaurant},
		{"unrelated key ignored", []Override{{Key: "other", Scope: ScopeRestaurant, Value: Value{Raw: "x"}}}, "30", ScopeDefinition},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Resolve(def, tc.overrides)
			if got.Value.Raw != tc.wantRaw {
				t.Errorf("raw = %q, want %q", got.Value.Raw, tc.wantRaw)
			}
			if got.SourceScope != tc.wantScope {
				t.Errorf("scope = %v, want %v", got.SourceScope, tc.wantScope)
			}
			if got.Key != def.Key {
				t.Errorf("key = %q, want %q", got.Key, def.Key)
			}
		})
	}
}

func TestNamespaceHelpers(t *testing.T) {
	if Namespace("billing.gst_pct") != "billing" {
		t.Errorf("namespace = %q", Namespace("billing.gst_pct"))
	}
	if Namespace("floor.nudge.greet_secs") != "floor.nudge" {
		t.Errorf("namespace = %q", Namespace("floor.nudge.greet_secs"))
	}
	if Namespace("solo") != "" {
		t.Errorf("single-segment namespace should be empty")
	}
	if !InNamespace("billing.gst_pct", "billing") {
		t.Error("billing.gst_pct should be in billing")
	}
	if InNamespace("billings.x", "billing") {
		t.Error("billings.x must NOT be in billing (dot boundary)")
	}
	if !InNamespace("anything", "") {
		t.Error("empty namespace matches everything")
	}
}
