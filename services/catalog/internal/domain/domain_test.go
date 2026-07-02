package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/catalog/internal/domain"
)

var now = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func TestNewCategory(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		cname   string
		wantErr bool
	}{
		{"ok new", "", "Mains", false},
		{"ok existing id", ids.New(domain.PrefixCategory), "Drinks", false},
		{"missing name", "", "  ", true},
		{"bad id", "nope", "Mains", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := domain.NewCategory(tc.id, tc.cname, 1)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixCategory, c.ID) {
				t.Fatalf("bad category id %q", c.ID)
			}
		})
	}
}

func TestNewItem(t *testing.T) {
	tests := []struct {
		name    string
		in      domain.NewItemInput
		wantErr bool
	}{
		{"ok", domain.NewItemInput{Name: "Paneer Tikka", Price: money.New(24000, "INR")}, false},
		{"default currency", domain.NewItemInput{Name: "Naan", Price: money.New(5000, "")}, false},
		{"missing name", domain.NewItemInput{Price: money.New(100, "INR")}, true},
		{"zero price", domain.NewItemInput{Name: "Free", Price: money.New(0, "INR")}, true},
		{"negative price", domain.NewItemInput{Name: "Bad", Price: money.New(-1, "INR")}, true},
		{"bad id", domain.NewItemInput{ID: "x", Name: "Y", Price: money.New(100, "INR")}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			it, err := domain.NewItem(tc.in, now)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixItem, it.ID) {
				t.Fatalf("bad item id %q", it.ID)
			}
			if !it.Available {
				t.Fatal("new item should default available")
			}
			if it.Price.Currency == "" {
				t.Fatal("currency should default to INR")
			}
		})
	}
}

func TestItemSetAvailability(t *testing.T) {
	it, _ := domain.NewItem(domain.NewItemInput{Name: "X", Price: money.New(100, "INR")}, now)
	if changed := it.SetAvailability(true); changed {
		t.Fatal("already available; should report no change")
	}
	if changed := it.SetAvailability(false); !changed {
		t.Fatal("should report changed when going unavailable")
	}
	if it.Available {
		t.Fatal("expected unavailable")
	}
}

func TestItemEffective_OutletOverride(t *testing.T) {
	it, _ := domain.NewItem(domain.NewItemInput{Name: "Biryani", Price: money.New(30000, "INR")}, now)

	// nil override => unchanged.
	if got := it.Effective(nil); got.Price.Minor != 30000 || !got.Available {
		t.Fatalf("nil override changed item: %+v", got)
	}

	// price override changes effective price.
	op := money.New(35000, "INR")
	got := it.Effective(&domain.OutletOverride{Price: &op})
	if got.Price.Minor != 35000 {
		t.Fatalf("override price = %d, want 35000", got.Price.Minor)
	}
	if !got.Available {
		t.Fatal("price-only override should not change availability")
	}

	// availability override takes the item offline at the outlet.
	got = it.Effective(&domain.OutletOverride{HasAvail: true, Available: false})
	if got.Available {
		t.Fatal("availability override should make item unavailable")
	}
	if got.Price.Minor != 30000 {
		t.Fatal("availability-only override should not change price")
	}
}

func TestEvaluate_DietaryConflicts(t *testing.T) {
	fish, _ := domain.NewItem(domain.NewItemInput{
		Name: "Fish Curry", Price: money.New(40000, "INR"),
		Tags: map[string]int32{"fish": 1},
	}, now)
	veg, _ := domain.NewItem(domain.NewItemInput{
		Name: "Dal Tadka", Price: money.New(18000, "INR"), Veg: true,
	}, now)

	tests := []struct {
		name    string
		item    domain.Item
		prefs   []string
		wantOK  bool
		wantLen int
	}{
		{"no prefs ok", fish, nil, true, 0},
		{"fish conflicts vegetarian", fish, []string{"vegetarian"}, false, 1},
		{"fish conflicts vegan twice? only fish flag", fish, []string{"vegan"}, false, 1},
		{"veg passes vegetarian", veg, []string{"vegetarian"}, true, 0},
		{"unknown pref ignored", fish, []string{"nonsense"}, true, 0},
		{"multiple prefs accumulate", fish, []string{"vegetarian", "pregnancy"}, false, 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := domain.Evaluate(tc.item, tc.prefs)
			if ev.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v (reasons=%v)", ev.OK, tc.wantOK, ev.Reasons)
			}
			if len(ev.Reasons) != tc.wantLen {
				t.Fatalf("reasons len = %d, want %d: %v", len(ev.Reasons), tc.wantLen, ev.Reasons)
			}
		})
	}
}

func TestItemHasFlag(t *testing.T) {
	it, _ := domain.NewItem(domain.NewItemInput{
		Name: "Cake", Price: money.New(20000, "INR"),
		Tags: map[string]int32{"dairy": 1, "egg": 0},
	}, now)
	if !it.HasFlag("dairy") {
		t.Fatal("dairy flag should be set")
	}
	if it.HasFlag("egg") {
		t.Fatal("egg flag value 0 should not count")
	}
	if it.HasFlag("nuts") {
		t.Fatal("absent flag should be false")
	}
}
