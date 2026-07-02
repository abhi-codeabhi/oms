package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/catalog/internal/app"
	"github.com/restorna/platform/services/catalog/internal/domain"
)

const rid = "out_test_outlet"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newApp(repo *fakeRepo) *app.App { return app.New(repo, fixedClock()) }

func seedItem(t *testing.T, a *app.App, repo *fakeRepo, in app.UpsertItemInput) domain.Item {
	t.Helper()
	it, err := a.UpsertItem(context.Background(), rid, in)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return it
}

func TestUpsertCategory(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	c, err := a.UpsertCategory(context.Background(), rid, "", "Mains", 2)
	if err != nil {
		t.Fatalf("upsert category: %v", err)
	}
	if !ids.Valid(domain.PrefixCategory, c.ID) {
		t.Fatalf("bad category id %q", c.ID)
	}
	if _, ok := repo.categories[c.ID]; !ok {
		t.Fatal("category not persisted")
	}

	// invalid: empty name.
	if _, err := a.UpsertCategory(context.Background(), rid, "", "  ", 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestListCategories_SortedBySortThenName(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	_, _ = a.UpsertCategory(context.Background(), rid, "", "Drinks", 3)
	_, _ = a.UpsertCategory(context.Background(), rid, "", "Appetizers", 1)
	_, _ = a.UpsertCategory(context.Background(), rid, "", "Mains", 1)

	cats, err := a.ListCategories(context.Background(), rid)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(cats) != 3 {
		t.Fatalf("want 3 cats, got %d", len(cats))
	}
	// sort 1 (Appetizers, Mains by name) then sort 3 (Drinks).
	if cats[0].Name != "Appetizers" || cats[1].Name != "Mains" || cats[2].Name != "Drinks" {
		t.Fatalf("bad order: %v", []string{cats[0].Name, cats[1].Name, cats[2].Name})
	}
}

func TestUpsertItem_CreateAndEmitMenuPublished(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "Paneer Tikka", Price: money.New(24000, "INR")})
	if !ids.Valid(domain.PrefixItem, it.ID) {
		t.Fatalf("bad item id %q", it.ID)
	}
	if !it.Available {
		t.Fatal("new item should be available")
	}
	if _, ok := repo.items[it.ID]; !ok {
		t.Fatal("item not persisted")
	}
	if got := countEvents(repo, app.EventMenuPublished); got != 1 {
		t.Fatalf("want 1 menu.published event on upsert, got %d", got)
	}
}

func TestUpsertItem_UpdatePreservesAvailability(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "Naan", Price: money.New(5000, "INR")})

	// 86 it at the outlet (override) ... brand availability is separate; instead
	// test the brand-level preserve: set brand availability false via repo, update.
	stored := repo.items[it.ID]
	stored.Available = false
	repo.items[it.ID] = stored

	updated, err := a.UpsertItem(context.Background(), rid, app.UpsertItemInput{
		ID: it.ID, Name: "Butter Naan", Price: money.New(6000, "INR"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Available {
		t.Fatal("update should preserve (false) brand availability")
	}
	if updated.Name != "Butter Naan" || updated.Price.Minor != 6000 {
		t.Fatalf("update did not apply fields: %+v", updated)
	}
}

func TestUpsertItem_InvalidRejected(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	if _, err := a.UpsertItem(context.Background(), rid, app.UpsertItemInput{Name: "", Price: money.New(100, "INR")}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing name err = %v, want ErrInvalid", err)
	}
	if _, err := a.UpsertItem(context.Background(), rid, app.UpsertItemInput{Name: "Free", Price: money.New(0, "INR")}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("zero price err = %v, want ErrInvalid", err)
	}
}

func TestGetMenu_FiltersUnavailableAndEvaluatesPrefs(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	avail := seedItem(t, a, repo, app.UpsertItemInput{Name: "Dal", Price: money.New(18000, "INR"), Veg: true})
	fish := seedItem(t, a, repo, app.UpsertItemInput{
		Name: "Fish Curry", Price: money.New(40000, "INR"),
		Tags: map[string]int32{"fish": 1},
	})
	hidden := seedItem(t, a, repo, app.UpsertItemInput{Name: "Hidden", Price: money.New(10000, "INR")})

	// 86 the hidden item at the outlet.
	if _, err := a.SetAvailability(context.Background(), rid, hidden.ID, false); err != nil {
		t.Fatalf("86: %v", err)
	}

	// only_available => hidden filtered out, 2 items remain.
	menu, err := a.GetMenu(context.Background(), rid, []string{"vegetarian"}, true)
	if err != nil {
		t.Fatalf("get menu: %v", err)
	}
	if len(menu) != 2 {
		t.Fatalf("want 2 available items, got %d", len(menu))
	}

	// dietary evaluation: fish flagged for vegetarian, dal ok.
	for _, e := range menu {
		switch e.Item.ID {
		case fish.ID:
			if e.OK || len(e.Reasons) == 0 {
				t.Fatalf("fish should conflict with vegetarian: %+v", e)
			}
		case avail.ID:
			if !e.OK {
				t.Fatalf("dal should pass vegetarian: %+v", e)
			}
		}
	}

	// only_available=false => all 3 returned (manager-style customer view).
	all, _ := a.GetMenu(context.Background(), rid, nil, false)
	if len(all) != 3 {
		t.Fatalf("want 3 items with only_available=false, got %d", len(all))
	}
}

func TestListAllItems_IncludesUnavailable(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	a1 := seedItem(t, a, repo, app.UpsertItemInput{Name: "A", Price: money.New(100, "INR")})
	_ = seedItem(t, a, repo, app.UpsertItemInput{Name: "B", Price: money.New(200, "INR")})
	if _, err := a.SetAvailability(context.Background(), rid, a1.ID, false); err != nil {
		t.Fatalf("86: %v", err)
	}
	items, err := a.ListAllItems(context.Background(), rid)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("manager view should include unavailable; got %d", len(items))
	}
}

func TestSetAvailability_TogglesAndEmits86(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "Soup", Price: money.New(12000, "INR")})

	// 86 => unavailable + event emitted.
	off, err := a.SetAvailability(context.Background(), rid, it.ID, false)
	if err != nil {
		t.Fatalf("86: %v", err)
	}
	if off.Available {
		t.Fatal("item should be unavailable after 86")
	}
	if got := countEvents(repo, app.EventItem86d); got != 1 {
		t.Fatalf("want 1 item.86d event, got %d", got)
	}

	// un-86 => available again, NO additional 86d event.
	on, err := a.SetAvailability(context.Background(), rid, it.ID, true)
	if err != nil {
		t.Fatalf("un-86: %v", err)
	}
	if !on.Available {
		t.Fatal("item should be available after un-86")
	}
	if got := countEvents(repo, app.EventItem86d); got != 1 {
		t.Fatalf("un-86 should not emit 86d; got %d total", got)
	}
}

func TestSetAvailability_UnknownItem(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	if _, err := a.SetAvailability(context.Background(), rid, ids.New(domain.PrefixItem), false); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetOutletOverride_ChangesEffectivePriceAndAvailability(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "Biryani", Price: money.New(30000, "INR")})

	// override price up + keep available.
	op := money.New(35000, "INR")
	out, err := a.SetOutletOverride(context.Background(), rid, it.ID, &op, true, false)
	if err != nil {
		t.Fatalf("override: %v", err)
	}
	if out.Price.Minor != 35000 {
		t.Fatalf("effective price = %d, want 35000", out.Price.Minor)
	}
	if !out.Available {
		t.Fatal("should remain available")
	}
	// brand item price untouched.
	if repo.items[it.ID].Price.Minor != 30000 {
		t.Fatal("brand price should not change")
	}

	// override taking item offline emits 86d.
	out, err = a.SetOutletOverride(context.Background(), rid, it.ID, nil, false, false)
	if err != nil {
		t.Fatalf("override offline: %v", err)
	}
	if out.Available {
		t.Fatal("override should make item unavailable")
	}
	if got := countEvents(repo, app.EventItem86d); got != 1 {
		t.Fatalf("offline override should emit 86d; got %d", got)
	}

	// clear => revert to brand defaults (price 30000, available true).
	cleared, err := a.SetOutletOverride(context.Background(), rid, it.ID, nil, false, true)
	if err != nil {
		t.Fatalf("clear: %v", err)
	}
	if cleared.Price.Minor != 30000 || !cleared.Available {
		t.Fatalf("clear should revert to brand defaults: %+v", cleared)
	}
	if _, ok := repo.overrides[it.ID]; ok {
		t.Fatal("override should be removed after clear")
	}
}

func TestSetOutletOverride_RejectsNonPositivePrice(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "X", Price: money.New(100, "INR")})
	bad := money.New(0, "INR")
	if _, err := a.SetOutletOverride(context.Background(), rid, it.ID, &bad, true, false); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
}

func TestGetItem(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	it := seedItem(t, a, repo, app.UpsertItemInput{Name: "Lassi", Price: money.New(8000, "INR"), Station: "cold"})

	got, err := a.GetItem(context.Background(), rid, it.ID)
	if err != nil {
		t.Fatalf("get item: %v", err)
	}
	if got.Name != "Lassi" || got.Station != "cold" {
		t.Fatalf("unexpected item: %+v", got)
	}
	if _, err := a.GetItem(context.Background(), rid, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty id err = %v, want ErrInvalid", err)
	}
	if _, err := a.GetItem(context.Background(), rid, ids.New(domain.PrefixItem)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing item err = %v, want ErrNotFound", err)
	}
}

func countEvents(repo *fakeRepo, typ string) int {
	n := 0
	for _, e := range repo.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}
