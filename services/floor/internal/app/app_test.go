package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/restorna/platform/services/floor/internal/app"
	"github.com/restorna/platform/services/floor/internal/domain"
	"github.com/restorna/platform/services/floor/internal/ports"
)

const rid = "out_01hx0000000000000000000000"

// fixedClock returns a deterministic epoch-ms clock for nudge timers.
func fixedClock(ms int64) app.Now { return func() int64 { return ms } }

func bg() context.Context { return context.Background() }

func TestInitFloor(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(1000))
	f, err := a.InitFloor(bg(), rid, []int32{1, 2, 3})
	if err != nil {
		t.Fatalf("InitFloor: %v", err)
	}
	if len(f.Tables) != 3 {
		t.Fatalf("want 3 tables, got %d", len(f.Tables))
	}
	if countEvents(repo, app.EventFloorInitialized) != 1 {
		t.Fatal("InitFloor should emit floor.initialized")
	}
	// Duplicate tables -> ErrInvalid.
	if _, err := a.InitFloor(bg(), rid, []int32{1, 1}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("dup tables: want ErrInvalid, got %v", err)
	}
}

func TestGetFloor_DerivedStatusPrecedence(t *testing.T) {
	repo := newFakeRepo()
	kitchen := &fakeKitchen{
		board: map[string][]ports.KitchenTicket{rid: {{Table: "T3"}}},        // table 3 cooking
		queue: map[string][]ports.KitchenTicket{rid: {{Table: "T2"}}},        // table 2 ready
	}
	billing := &fakeBilling{open: map[string][]ports.OpenBill{rid: {{Table: "T1"}}}} // table 1 billing
	a := app.New(repo, kitchen, billing, nil, nil, fixedClock(1000))

	// Seat tables 1-4 so the floor exists; 4 stays idle (seated), others derived.
	if _, err := a.InitFloor(bg(), rid, []int32{1, 2, 3, 4}); err != nil {
		t.Fatalf("init: %v", err)
	}
	for _, n := range []int32{1, 2, 3, 4} {
		if _, err := a.SeatParty(bg(), rid, n); err != nil {
			t.Fatalf("seat %d: %v", n, err)
		}
	}

	f, err := a.GetFloor(bg(), rid)
	if err != nil {
		t.Fatalf("GetFloor: %v", err)
	}
	want := map[int32]string{
		1: domain.StatusBilling, // open bill wins over everything
		2: domain.StatusReady,   // ready ticket
		3: domain.StatusCooking, // cooking ticket
		4: domain.StatusSeated,  // occupied, nothing outstanding
	}
	for _, tbl := range f.Tables {
		if got := want[tbl.N]; tbl.Status != got {
			t.Fatalf("table %d derived status = %q, want %q", tbl.N, tbl.Status, got)
		}
	}
}

func TestGetFloor_FreeWhenEmpty(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, &fakeKitchen{}, &fakeBilling{}, nil, nil, fixedClock(1000))
	if _, err := a.InitFloor(bg(), rid, []int32{1}); err != nil {
		t.Fatalf("init: %v", err)
	}
	f, _ := a.GetFloor(bg(), rid)
	if f.Tables[0].Status != domain.StatusFree {
		t.Fatalf("unoccupied table should derive free, got %q", f.Tables[0].Status)
	}
}

func TestSeatParty_ArmsGreetTimer(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(5000))
	// SeatParty also ensures the table even if the floor was never initialised.
	if _, err := a.SeatParty(bg(), rid, 7); err != nil {
		t.Fatalf("SeatParty: %v", err)
	}
	f, _ := repo.Get(bg(), rid)
	tbl := f.Find(7)
	if tbl == nil {
		t.Fatal("SeatParty should create the table")
	}
	if tbl.Status != domain.StatusSeated || tbl.SeatedAt != 5000 {
		t.Fatalf("seat should arm greet timer: %+v", *tbl)
	}
	if countEvents(repo, app.EventTableSeated) != 1 {
		t.Fatal("SeatParty should emit table.seated")
	}
}

func TestAssignWaiter_MultiTable(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(1))
	_, _ = a.InitFloor(bg(), rid, []int32{1, 2, 3})
	if _, err := a.AssignWaiter(bg(), rid, []int32{1, 3}, "stf_x"); err != nil {
		t.Fatalf("AssignWaiter: %v", err)
	}
	f, _ := repo.Get(bg(), rid)
	if f.Find(1).WaiterID != "stf_x" || f.Find(3).WaiterID != "stf_x" {
		t.Fatal("both tables should be assigned")
	}
	if f.Find(2).WaiterID != "" {
		t.Fatal("table 2 should be untouched")
	}
	// Missing table -> ErrNotFound, nothing committed.
	if _, err := a.AssignWaiter(bg(), rid, []int32{2, 9}, "stf_y"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	f2, _ := repo.Get(bg(), rid)
	if f2.Find(2).WaiterID != "" {
		t.Fatal("failed multi-assign must not partially commit")
	}
}

func TestMove_RelocatesSeatAndCallsOrdering(t *testing.T) {
	repo := newFakeRepo()
	ordering := &fakeOrdering{}
	a := app.New(repo, nil, nil, nil, ordering, fixedClock(1000))
	_, _ = a.InitFloor(bg(), rid, []int32{1, 2})
	_, _ = a.SeatParty(bg(), rid, 1)
	_, _ = a.AssignWaiter(bg(), rid, []int32{1}, "stf_a")

	res, err := a.Move(bg(), rid, 1, 2)
	if err != nil {
		t.Fatalf("Move: %v", err)
	}
	if res.Verb != "moved" {
		t.Fatalf("verb = %q, want moved", res.Verb)
	}
	// Seat relocated in the doc.
	f, _ := repo.Get(bg(), rid)
	if f.Find(2).WaiterID != "stf_a" || f.Find(2).Status != domain.StatusSeated {
		t.Fatalf("seat did not relocate: %+v", *f.Find(2))
	}
	if f.Find(1).Status != domain.StatusFree {
		t.Fatalf("source not freed: %+v", *f.Find(1))
	}
	// Ordering.Relocate called T1 -> T2.
	if len(ordering.calls) != 1 || ordering.calls[0] != (relocateCall{From: "T1", To: "T2"}) {
		t.Fatalf("ordering.Relocate not called correctly: %+v", ordering.calls)
	}
	if countEvents(repo, app.EventTableMoved) != 1 {
		t.Fatal("Move should emit table.moved")
	}
}

func TestMove_SwapRelocatesBothViaScratch(t *testing.T) {
	repo := newFakeRepo()
	ordering := &fakeOrdering{}
	a := app.New(repo, nil, nil, nil, ordering, fixedClock(1000))
	_, _ = a.InitFloor(bg(), rid, []int32{1, 2})
	_, _ = a.SeatParty(bg(), rid, 1)
	_, _ = a.SeatParty(bg(), rid, 2)

	res, err := a.Move(bg(), rid, 1, 2)
	if err != nil {
		t.Fatalf("Move(swap): %v", err)
	}
	if res.Verb != "swapped" {
		t.Fatalf("verb = %q, want swapped", res.Verb)
	}
	// Swap performs three hops so neither table's orders clobber the other.
	if len(ordering.calls) != 3 {
		t.Fatalf("swap should make 3 relocate hops, got %+v", ordering.calls)
	}
}

func TestGetNudges_UsesSettingsConfig(t *testing.T) {
	repo := newFakeRepo()
	// Greet delay 10s via settings; seat at t0, query at t0+15s -> greet fires.
	c := domain.DefaultNudgeConfig()
	c.GreetDelaySecs = 10
	settings := &fakeSettings{cfg: &c}
	a := app.New(repo, nil, nil, settings, nil, fixedClock(15_000))
	// Seat at 0 (separate app instance with a 0 clock to set seatedAt=0... use repo directly).
	f, _ := domain.NewFloor([]int32{1})
	_ = f.Seat(1, "ord", 0)
	_ = repo.Save(bg(), rid, f)

	nudges, err := a.GetNudges(bg(), rid)
	if err != nil {
		t.Fatalf("GetNudges: %v", err)
	}
	if len(nudges) != 1 || nudges[0].Type != domain.NudgeGreet {
		t.Fatalf("greet should fire with 10s settings delay at 15s: %+v", nudges)
	}
}

func TestAckNudge_SetsTimestamps(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(9000))
	f, _ := domain.NewFloor([]int32{1})
	_ = f.Seat(1, "ord", 0)
	_ = repo.Save(bg(), rid, f)

	// Greet ack sets greetedAt.
	if err := a.AckNudge(bg(), rid, 1, domain.NudgeGreet); err != nil {
		t.Fatalf("AckNudge greet: %v", err)
	}
	got, _ := repo.Get(bg(), rid)
	if got.Find(1).GreetedAt != 9000 {
		t.Fatalf("greet ack should set greetedAt=9000, got %d", got.Find(1).GreetedAt)
	}

	// Checkin ack sets lastCheckinAt.
	if err := a.AckNudge(bg(), rid, 1, domain.NudgeCheckin); err != nil {
		t.Fatalf("AckNudge checkin: %v", err)
	}
	got, _ = repo.Get(bg(), rid)
	if got.Find(1).LastCheckinAt != 9000 {
		t.Fatalf("checkin ack should set lastCheckinAt=9000, got %d", got.Find(1).LastCheckinAt)
	}

	// Unknown type -> ErrInvalid.
	if err := a.AckNudge(bg(), rid, 1, "nope"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown nudge type: want ErrInvalid, got %v", err)
	}
	// Missing table -> ErrNotFound.
	if err := a.AckNudge(bg(), rid, 9, domain.NudgeGreet); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing table: want ErrNotFound, got %v", err)
	}
}

func TestOnOrderPlaced_SeatsAndIsIdempotent(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(2000))

	if err := a.OnOrderPlaced(bg(), rid, "evt_1", "T5", "ord_1"); err != nil {
		t.Fatalf("OnOrderPlaced: %v", err)
	}
	f, _ := repo.Get(bg(), rid)
	tbl := f.Find(5)
	if tbl == nil || tbl.Status != domain.StatusSeated || tbl.SeatedAt != 2000 || tbl.Order != "ord_1" {
		t.Fatalf("order.placed should ensure+seat table 5: %+v", tbl)
	}

	// Re-deliver same event id: no second seat, seatedAt unchanged.
	if err := a.OnOrderPlaced(bg(), rid, "evt_1", "T5", "ord_1"); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	f2, _ := repo.Get(bg(), rid)
	if f2.Find(5).SeatedAt != 2000 {
		t.Fatalf("redelivery must be a no-op, seatedAt=%d", f2.Find(5).SeatedAt)
	}
}

func TestOnTicketServed_SetsLastServedAt(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock(4000))
	_, _ = a.SeatParty(bg(), rid, 8)

	if err := a.OnTicketServed(bg(), rid, "evt_2", "T8"); err != nil {
		t.Fatalf("OnTicketServed: %v", err)
	}
	f, _ := repo.Get(bg(), rid)
	if f.Find(8).LastServedAt != 4000 {
		t.Fatalf("served event should set lastServedAt=4000, got %d", f.Find(8).LastServedAt)
	}
	// Idempotent on redelivery.
	a2 := app.New(repo, nil, nil, nil, nil, fixedClock(9999))
	if err := a2.OnTicketServed(bg(), rid, "evt_2", "T8"); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	f2, _ := repo.Get(bg(), rid)
	if f2.Find(8).LastServedAt != 4000 {
		t.Fatalf("served redelivery must be a no-op, lastServedAt=%d", f2.Find(8).LastServedAt)
	}
}
