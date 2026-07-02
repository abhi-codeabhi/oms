package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/kitchen/internal/app"
	"github.com/restorna/platform/services/kitchen/internal/domain"
	"github.com/restorna/platform/services/kitchen/internal/ports"
)

const rid = "out_01hx0000000000000000000000"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func receive(t *testing.T, a *app.App, table string, items ...app.ReceiveItemInput) domain.Ticket {
	t.Helper()
	tk, err := a.ReceiveTicket(context.Background(), rid, app.ReceiveTicketInput{
		OrderID: ids.New("ord"), Table: table, Items: items,
	})
	if err != nil {
		t.Fatalf("ReceiveTicket: %v", err)
	}
	return tk
}

func TestReceiveTicket(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	tk := receive(t, a, "T7", app.ReceiveItemInput{Name: "Paneer Tikka", Station: "tandoor"})
	if !ids.Valid(domain.PrefixTicket, tk.ID) {
		t.Fatalf("ticket id %q invalid", tk.ID)
	}
	if tk.Items[0].State != domain.StateNew {
		t.Fatal("new item should start at StateNew")
	}
	all, _ := repo.List(context.Background(), rid)
	if len(all) != 1 {
		t.Fatalf("want 1 persisted ticket, got %d", len(all))
	}
}

func TestAdvanceItem_States(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	tk := receive(t, a, "T1", app.ReceiveItemInput{Name: "Curry", Station: "grill"})

	for _, want := range []domain.ItemState{domain.StatePrep, domain.StateReady, domain.StateReady} {
		got, err := a.AdvanceItem(context.Background(), rid, tk.ID, 0)
		if err != nil {
			t.Fatalf("advance: %v", err)
		}
		if got.Items[0].State != want {
			t.Fatalf("state = %d, want %d", got.Items[0].State, want)
		}
	}
}

func TestAdvanceItem_LastItemReadyEmitsReadyEvent(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	tk := receive(t, a, "T1",
		app.ReceiveItemInput{Name: "A", Station: "grill"},
		app.ReceiveItemInput{Name: "B", Station: "cold"},
	)
	// Drive item 0 to ready -> still cooking, no event yet.
	a.AdvanceItem(context.Background(), rid, tk.ID, 0)
	a.AdvanceItem(context.Background(), rid, tk.ID, 0)
	if got := countEvents(repo, app.EventTicketReady); got != 0 {
		t.Fatalf("partial ready should emit 0 ready events, got %d", got)
	}
	// Drive item 1 to ready -> whole ticket ready -> exactly one ready event.
	a.AdvanceItem(context.Background(), rid, tk.ID, 1)
	a.AdvanceItem(context.Background(), rid, tk.ID, 1)
	if got := countEvents(repo, app.EventTicketReady); got != 1 {
		t.Fatalf("last item ready should emit 1 ready event, got %d", got)
	}
}

func TestBump_MarksAllReadyAndEmitsReadyEvent(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	tk := receive(t, a, "T1",
		app.ReceiveItemInput{Name: "A", Station: "grill"},
		app.ReceiveItemInput{Name: "B", Station: "tandoor"},
	)
	got, err := a.Bump(context.Background(), rid, tk.ID)
	if err != nil {
		t.Fatalf("bump: %v", err)
	}
	if !got.IsAllReady() {
		t.Fatal("bump should mark all items ready")
	}
	if n := countEvents(repo, app.EventTicketReady); n != 1 {
		t.Fatalf("bump should emit exactly 1 ready event, got %d", n)
	}
	// Bumping again must not re-emit (was already ready).
	a.Bump(context.Background(), rid, tk.ID)
	if n := countEvents(repo, app.EventTicketReady); n != 1 {
		t.Fatalf("re-bump should not re-emit, got %d ready events", n)
	}
}

func TestGetBoard_ExcludesReadyTickets(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	cooking := receive(t, a, "T1", app.ReceiveItemInput{Name: "Cooking"})
	ready := receive(t, a, "T2", app.ReceiveItemInput{Name: "Ready"})
	a.Bump(context.Background(), rid, ready.ID)

	board, err := a.GetBoard(context.Background(), rid)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	if len(board) != 1 || board[0].ID != cooking.ID {
		t.Fatalf("board should hold only the cooking ticket, got %d", len(board))
	}
}

func TestServeQueue_ReadyUnserved(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	_ = receive(t, a, "T1", app.ReceiveItemInput{Name: "StillCooking"})
	ready := receive(t, a, "T2", app.ReceiveItemInput{Name: "Ready"})
	a.Bump(context.Background(), rid, ready.ID)
	served := receive(t, a, "T3", app.ReceiveItemInput{Name: "Served"})
	a.Bump(context.Background(), rid, served.ID)
	a.Serve(context.Background(), rid, served.ID)

	q, err := a.ServeQueue(context.Background(), rid)
	if err != nil {
		t.Fatalf("serve queue: %v", err)
	}
	if len(q) != 1 || q[0].ID != ready.ID {
		t.Fatalf("serve queue should hold only the ready-unserved ticket, got %d", len(q))
	}
}

func TestServe_MarksOnlyOneTicketAtTheTable(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	// Two rounds at the SAME table, both ready.
	round1 := receive(t, a, "T5", app.ReceiveItemInput{Name: "Round1"})
	round2 := receive(t, a, "T5", app.ReceiveItemInput{Name: "Round2"})
	a.Bump(context.Background(), rid, round1.ID)
	a.Bump(context.Background(), rid, round2.ID)

	served, err := a.Serve(context.Background(), rid, round1.ID)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	if !served.Served {
		t.Fatal("served ticket should be marked served")
	}
	// round2 at the same table must remain unserved and in the queue.
	q, _ := a.ServeQueue(context.Background(), rid)
	if len(q) != 1 || q[0].ID != round2.ID {
		t.Fatalf("serving round1 must leave round2 at the table; queue=%d", len(q))
	}
	if n := countEvents(repo, app.EventTicketServed); n != 1 {
		t.Fatalf("want 1 served event, got %d", n)
	}
}

func TestAllDay(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeCatalog(), fixedClock())
	receive(t, a, "T1", app.ReceiveItemInput{Name: "Naan"}, app.ReceiveItemInput{Name: "Dal"})
	t2 := receive(t, a, "T2", app.ReceiveItemInput{Name: "Naan"})
	a.Bump(context.Background(), rid, t2.ID) // Naan on t2 ready -> not counted

	counts, err := a.AllDay(context.Background(), rid)
	if err != nil {
		t.Fatalf("all day: %v", err)
	}
	if counts["Naan"] != 1 || counts["Dal"] != 1 {
		t.Fatalf("unexpected all-day counts: %+v", counts)
	}
}

func TestOnOrderPlaced_CreatesTicketResolvingNames(t *testing.T) {
	repo := newFakeRepo()
	cat := newFakeCatalog()
	cat.items["item_paneer"] = ports.ResolvedItem{Name: "Paneer Tikka", Station: "tandoor"}
	cat.items["item_fries"] = ports.ResolvedItem{Name: "Masala Fries", Station: "grill"}
	a := app.New(repo, cat, fixedClock())

	err := a.OnOrderPlaced(context.Background(), app.OrderPlaced{
		EventID:      "evt_1",
		OrderID:      "ord_99",
		RestaurantID: rid,
		Table:        "T7",
		Lines: []app.OrderPlacedLine{
			{MenuItemID: "item_paneer", Qty: 1},
			{MenuItemID: "item_fries", Qty: 2},
		},
	})
	if err != nil {
		t.Fatalf("OnOrderPlaced: %v", err)
	}

	all, _ := repo.List(context.Background(), rid)
	if len(all) != 1 {
		t.Fatalf("want 1 ticket from order, got %d", len(all))
	}
	tk := all[0]
	if tk.OrderID != "ord_99" || tk.Table != "T7" {
		t.Fatalf("ticket order/table mismatch: %+v", tk)
	}
	// 1 paneer + 2 fries = 3 items, names resolved from catalog.
	if len(tk.Items) != 3 {
		t.Fatalf("want 3 ticket items (qty expanded), got %d", len(tk.Items))
	}
	names := map[string]string{}
	for _, it := range tk.Items {
		names[it.Name] = it.Station
	}
	if names["Paneer Tikka"] != "tandoor" {
		t.Fatalf("paneer not resolved to tandoor: %+v", names)
	}
	if names["Masala Fries"] != "grill" {
		t.Fatalf("fries not resolved to grill: %+v", names)
	}
}

func TestOnOrderPlaced_Idempotent(t *testing.T) {
	repo := newFakeRepo()
	cat := newFakeCatalog()
	cat.items["item_x"] = ports.ResolvedItem{Name: "Thing", Station: "cold"}
	a := app.New(repo, cat, fixedClock())

	ev := app.OrderPlaced{
		EventID: "evt_dup", OrderID: "ord_1", RestaurantID: rid, Table: "T1",
		Lines: []app.OrderPlacedLine{{MenuItemID: "item_x", Qty: 1}},
	}
	// The app stages the processed mark; a real consumer also dedupes on Event.ID.
	// Here we assert the processed mark is recorded so the consumer can skip dups.
	if err := a.OnOrderPlaced(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	if _, ok := repo.processed[rid+"|evt_dup"]; !ok {
		t.Fatal("event id should be marked processed in the same tx as the ticket")
	}
}

func TestOnOrderPlaced_FallsBackToLineNameWhenCatalogMisses(t *testing.T) {
	repo := newFakeRepo()
	cat := newFakeCatalog() // empty -> Resolve returns ErrNotFound
	a := app.New(repo, cat, fixedClock())

	err := a.OnOrderPlaced(context.Background(), app.OrderPlaced{
		EventID: "evt_2", OrderID: "ord_2", RestaurantID: rid, Table: "T2",
		Lines: []app.OrderPlacedLine{{MenuItemID: "item_gone", Name: "Fallback Dish", Station: "grill", Qty: 1}},
	})
	if err != nil {
		t.Fatalf("OnOrderPlaced: %v", err)
	}
	all, _ := repo.List(context.Background(), rid)
	if len(all) != 1 || all[0].Items[0].Name != "Fallback Dish" {
		t.Fatalf("should fall back to the line's own name when catalog misses; got %+v", all)
	}
}

func TestOnOrderPlaced_Validation(t *testing.T) {
	a := app.New(newFakeRepo(), newFakeCatalog(), fixedClock())
	cases := []app.OrderPlaced{
		{EventID: "e", RestaurantID: "", Lines: []app.OrderPlacedLine{{Name: "x"}}}, // no restaurant
		{EventID: "e", RestaurantID: rid, Lines: nil},                               // no lines
	}
	for i, ev := range cases {
		if err := a.OnOrderPlaced(context.Background(), ev); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}
