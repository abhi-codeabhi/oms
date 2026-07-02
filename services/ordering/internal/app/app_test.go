package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/ordering/internal/app"
	"github.com/restorna/platform/services/ordering/internal/domain"
)

const rid = "out_acme"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func newApp(repo *fakeRepo) *app.App { return app.New(repo, fixedClock()) }

func line(menuItem string, qty int32, minor int64) domain.NewLineInput {
	return domain.NewLineInput{MenuItemID: menuItem, Qty: qty, UnitPrice: money.New(minor, "INR")}
}

// place is a helper that places an order on a table, failing the test on error.
func place(t *testing.T, a *app.App, repo *fakeRepo, table string, items ...domain.NewLineInput) domain.Order {
	t.Helper()
	o, err := a.PlaceOrder(context.Background(), app.PlaceOrderInput{RestaurantID: rid, TableID: table, Items: items})
	if err != nil {
		t.Fatalf("place order: %v", err)
	}
	return o
}

func TestPlaceOrder_ComputesSubtotalAndStagesEvent(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	o, err := a.PlaceOrder(context.Background(), app.PlaceOrderInput{
		RestaurantID: rid,
		TableID:      "T7",
		Items:        []domain.NewLineInput{line("itm_a", 2, 12000), line("itm_b", 1, 5000)},
	})
	if err != nil {
		t.Fatalf("place order: %v", err)
	}
	if !ids.Valid(domain.PrefixOrder, o.ID) {
		t.Fatalf("order id %q invalid", o.ID)
	}
	if o.Subtotal.Minor != 29000 {
		t.Fatalf("subtotal = %d, want 29000", o.Subtotal.Minor)
	}
	if _, ok := repo.orders[o.ID]; !ok {
		t.Fatal("order not persisted")
	}
	// order.placed must be staged (so kitchen + floor react).
	if got := countEvents(repo, app.EventOrderPlaced); got != 1 {
		t.Fatalf("want 1 order.placed event, got %d", got)
	}
	// event payload carries order_id / restaurant_id / table_id / lines.
	ev := lastEvent(repo, app.EventOrderPlaced)
	data, ok := ev.Data.(map[string]any)
	if !ok {
		t.Fatalf("event data type = %T", ev.Data)
	}
	if data["order_id"] != o.ID || data["restaurant_id"] != rid || data["table_id"] != "T7" {
		t.Fatalf("event header mismatch: %+v", data)
	}
	lines, ok := data["lines"].([]map[string]any)
	if !ok || len(lines) != 2 {
		t.Fatalf("event lines = %v, want 2", data["lines"])
	}
}

func TestPlaceOrder_InvalidRollsBackNoEvent(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	// empty items -> domain validation error, nothing persisted/staged.
	_, err := a.PlaceOrder(context.Background(), app.PlaceOrderInput{RestaurantID: rid, TableID: "T1"})
	if !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if len(repo.orders) != 0 || len(repo.events) != 0 {
		t.Fatal("nothing should be persisted or staged on invalid input")
	}
}

func TestPlaceOrder_PersistFailureDiscardsEvent(t *testing.T) {
	repo := newFakeRepo()
	repo.failInsert = true
	a := newApp(repo)
	_, err := a.PlaceOrder(context.Background(), app.PlaceOrderInput{
		RestaurantID: rid, TableID: "T1", Items: []domain.NewLineInput{line("a", 1, 100)},
	})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	// transactional outbox: the staged event must NOT commit when the write fails.
	if len(repo.events) != 0 {
		t.Fatalf("event should not be committed on rollback, got %d", len(repo.events))
	}
}

func TestGetOrder(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	o := place(t, a, repo, "T3", line("a", 1, 100))

	got, err := a.GetOrder(context.Background(), rid, o.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != o.ID {
		t.Fatalf("got %q, want %q", got.ID, o.ID)
	}
	// invalid id -> ErrInvalid; unknown id -> ErrNotFound.
	if _, err := a.GetOrder(context.Background(), rid, "nope"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad id err = %v, want ErrInvalid", err)
	}
	if _, err := a.GetOrder(context.Background(), rid, ids.New(domain.PrefixOrder)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing id err = %v, want ErrNotFound", err)
	}
}

func TestListForTable_TolerantMatchAndExcludesBilled(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	o1 := place(t, a, repo, "T7", line("a", 1, 100))  // matches "7"
	o2 := place(t, a, repo, "7", line("b", 1, 100))   // matches "7"
	_ = place(t, a, repo, "T8", line("c", 1, 100))    // different table
	o4 := place(t, a, repo, "Table 7", line("d", 1, 100)) // matches "7"

	// query by "7" must find T7, 7 and "Table 7" (tolerant), but not T8.
	got, err := a.ListForTable(context.Background(), rid, "7", false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 orders for table 7, got %d", len(got))
	}
	seen := map[string]bool{}
	for _, o := range got {
		seen[o.ID] = true
	}
	if !seen[o1.ID] || !seen[o2.ID] || !seen[o4.ID] {
		t.Fatalf("tolerant match missed an order: %+v", seen)
	}

	// querying by "T7" gives the same set (tolerant on the query side too).
	byLabel, _ := a.ListForTable(context.Background(), rid, "T7", false)
	if len(byLabel) != 3 {
		t.Fatalf("T7 query want 3, got %d", len(byLabel))
	}

	// bill one; default (unbilled) list drops it, includeBilled keeps it.
	if _, err := a.MarkBilled(context.Background(), rid, []string{o1.ID}); err != nil {
		t.Fatalf("mark billed: %v", err)
	}
	unbilled, _ := a.ListForTable(context.Background(), rid, "7", false)
	if len(unbilled) != 2 {
		t.Fatalf("after billing one, unbilled want 2, got %d", len(unbilled))
	}
	withBilled, _ := a.ListForTable(context.Background(), rid, "7", true)
	if len(withBilled) != 3 {
		t.Fatalf("includeBilled want 3, got %d", len(withBilled))
	}
}

func TestMarkBilled(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	o1 := place(t, a, repo, "T1", line("a", 1, 100))
	o2 := place(t, a, repo, "T1", line("b", 1, 100))

	n, err := a.MarkBilled(context.Background(), rid, []string{o1.ID, o2.ID})
	if err != nil {
		t.Fatalf("mark billed: %v", err)
	}
	if n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
	if !repo.orders[o1.ID].Billed || !repo.orders[o2.ID].Billed {
		t.Fatal("orders not flagged billed")
	}

	// idempotent: re-billing already-billed orders flips nothing.
	n2, _ := a.MarkBilled(context.Background(), rid, []string{o1.ID})
	if n2 != 0 {
		t.Fatalf("re-bill count = %d, want 0", n2)
	}

	// unknown / invalid ids are skipped, not fatal.
	n3, err := a.MarkBilled(context.Background(), rid, []string{"garbage", ids.New(domain.PrefixOrder)})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if n3 != 0 {
		t.Fatalf("count = %d, want 0", n3)
	}
}

func TestRelocate_MovesUnbilledOrders(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)

	o1 := place(t, a, repo, "T5", line("a", 1, 100))
	o2 := place(t, a, repo, "5", line("b", 1, 100)) // tolerant: also on table 5
	billed := place(t, a, repo, "T5", line("c", 1, 100))
	other := place(t, a, repo, "T9", line("d", 1, 100))
	if _, err := a.MarkBilled(context.Background(), rid, []string{billed.ID}); err != nil {
		t.Fatalf("seed billed: %v", err)
	}

	moved, err := a.Relocate(context.Background(), rid, "T5", "T12")
	if err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if moved != 2 {
		t.Fatalf("moved = %d, want 2 (unbilled on T5 only)", moved)
	}
	if repo.orders[o1.ID].TableID != "T12" || repo.orders[o2.ID].TableID != "T12" {
		t.Fatal("unbilled orders not moved to T12")
	}
	if repo.orders[billed.ID].TableID != "T5" {
		t.Fatal("billed order should NOT move")
	}
	if repo.orders[other.ID].TableID != "T9" {
		t.Fatal("order on another table should NOT move")
	}
}

func TestRelocate_ValidatesTargets(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo)
	if _, err := a.Relocate(context.Background(), rid, "", "T1"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty from err = %v, want ErrInvalid", err)
	}
	if _, err := a.Relocate(context.Background(), rid, "T1", "  "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty to err = %v, want ErrInvalid", err)
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

func lastEvent(repo *fakeRepo, typ string) stagedEvent {
	var out stagedEvent
	for _, e := range repo.events {
		if e.Type == typ {
			out = e
		}
	}
	return out
}
