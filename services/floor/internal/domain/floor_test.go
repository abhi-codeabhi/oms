package domain_test

import (
	"errors"
	"testing"

	"github.com/restorna/platform/services/floor/internal/domain"
)

func TestNewFloor_RejectsDuplicates(t *testing.T) {
	if _, err := domain.NewFloor([]int32{1, 2, 2}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for duplicate table, got %v", err)
	}
	f, err := domain.NewFloor([]int32{3, 1, 2})
	if err != nil {
		t.Fatalf("NewFloor: %v", err)
	}
	// Sorted by number.
	for i, want := range []int32{1, 2, 3} {
		if f.Tables[i].N != want {
			t.Fatalf("tables[%d].N = %d, want %d", i, f.Tables[i].N, want)
		}
		if f.Tables[i].Status != domain.StatusFree {
			t.Fatalf("new table should be free")
		}
	}
}

func TestSeat_ArmsGreetTimerOnce(t *testing.T) {
	f, _ := domain.NewFloor([]int32{7})
	if err := f.Seat(7, "ord_1", 1000); err != nil {
		t.Fatalf("Seat: %v", err)
	}
	tbl := f.Find(7)
	if tbl.Status != domain.StatusSeated {
		t.Fatalf("status = %q, want seated", tbl.Status)
	}
	if tbl.SeatedAt != 1000 {
		t.Fatalf("seatedAt = %d, want 1000 (greet timer armed)", tbl.SeatedAt)
	}
	if tbl.Order != "ord_1" {
		t.Fatalf("order = %q, want ord_1", tbl.Order)
	}
	// Re-seating (next round) must NOT reset the greet timer.
	if err := f.Seat(7, "ord_2", 5000); err != nil {
		t.Fatalf("Seat round 2: %v", err)
	}
	if tbl.SeatedAt != 1000 {
		t.Fatalf("seatedAt reset to %d on re-seat; should stay 1000", tbl.SeatedAt)
	}
	if tbl.Order != "ord_2" {
		t.Fatalf("order = %q, want ord_2 (updated)", tbl.Order)
	}
}

func TestSeat_NotFound(t *testing.T) {
	f, _ := domain.NewFloor([]int32{1})
	if err := f.Seat(9, "", 1); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestEnsureTable(t *testing.T) {
	f, _ := domain.NewFloor([]int32{1})
	if !f.EnsureTable(5) {
		t.Fatal("EnsureTable(5) should add a new table")
	}
	if f.EnsureTable(5) {
		t.Fatal("EnsureTable(5) again should be a no-op")
	}
	if f.Find(5) == nil {
		t.Fatal("table 5 should exist after EnsureTable")
	}
}

func TestDeriveStatus_Precedence(t *testing.T) {
	seated := domain.Table{N: 1, Status: domain.StatusSeated, Order: "ord_1"}
	free := domain.Table{N: 2, Status: domain.StatusFree}

	tests := []struct {
		name        string
		table       domain.Table
		load        domain.TableLoad
		hasOpenBill bool
		want        string
	}{
		{"billing beats everything", seated, domain.TableLoad{Cooking: 2, Ready: 1}, true, domain.StatusBilling},
		{"ready beats cooking", seated, domain.TableLoad{Cooking: 3, Ready: 1}, false, domain.StatusReady},
		{"cooking beats seated", seated, domain.TableLoad{Cooking: 1}, false, domain.StatusCooking},
		{"seated when occupied idle", seated, domain.TableLoad{}, false, domain.StatusSeated},
		{"free when empty", free, domain.TableLoad{}, false, domain.StatusFree},
		// A free table with no order but live tickets still derives by load.
		{"free table with cooking load shows cooking", free, domain.TableLoad{Cooking: 1}, false, domain.StatusCooking},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.DeriveStatus(tt.table, tt.load, tt.hasOpenBill); got != tt.want {
				t.Fatalf("DeriveStatus = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTableNumber(t *testing.T) {
	tests := []struct {
		in   string
		want int32
	}{
		{"T7", 7}, {"7", 7}, {"Table 12", 12}, {"", 0}, {"VIP", 0}, {"T03", 3},
	}
	for _, tt := range tests {
		if got := domain.TableNumber(tt.in); got != tt.want {
			t.Fatalf("TableNumber(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestMoveOrSwap_Move(t *testing.T) {
	f, _ := domain.NewFloor([]int32{1, 2})
	_ = f.Seat(1, "ord_1", 1000)
	_ = f.Assign(1, "stf_a")
	f.Find(1).LastServedAt = 2000

	verb, err := f.MoveOrSwap(1, 2)
	if err != nil {
		t.Fatalf("MoveOrSwap: %v", err)
	}
	if verb != "moved" {
		t.Fatalf("verb = %q, want moved", verb)
	}
	src, dst := f.Find(1), f.Find(2)
	if src.Status != domain.StatusFree || src.Order != "" || src.WaiterID != "" || src.SeatedAt != 0 {
		t.Fatalf("src not reset after move: %+v", *src)
	}
	if dst.Order != "ord_1" || dst.WaiterID != "stf_a" || dst.SeatedAt != 1000 || dst.LastServedAt != 2000 {
		t.Fatalf("dst did not inherit seat+timers: %+v", *dst)
	}
}

func TestMoveOrSwap_Swap(t *testing.T) {
	f, _ := domain.NewFloor([]int32{1, 2})
	_ = f.Seat(1, "ord_1", 1000)
	_ = f.Assign(1, "stf_a")
	_ = f.Seat(2, "ord_2", 1500)
	_ = f.Assign(2, "stf_b")

	verb, err := f.MoveOrSwap(1, 2)
	if err != nil {
		t.Fatalf("MoveOrSwap: %v", err)
	}
	if verb != "swapped" {
		t.Fatalf("verb = %q, want swapped", verb)
	}
	if f.Find(1).Order != "ord_2" || f.Find(1).WaiterID != "stf_b" {
		t.Fatalf("table 1 should now hold table 2's seat: %+v", *f.Find(1))
	}
	if f.Find(2).Order != "ord_1" || f.Find(2).WaiterID != "stf_a" {
		t.Fatalf("table 2 should now hold table 1's seat: %+v", *f.Find(2))
	}
	// Numbers must be preserved.
	if f.Find(1).N != 1 || f.Find(2).N != 2 {
		t.Fatal("table numbers must not change on swap")
	}
}

func TestMoveOrSwap_Errors(t *testing.T) {
	f, _ := domain.NewFloor([]int32{1, 2})
	if _, err := f.MoveOrSwap(1, 1); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("same table: want ErrInvalid, got %v", err)
	}
	if _, err := f.MoveOrSwap(1, 2); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("free source: want ErrInvalid, got %v", err)
	}
	_ = f.Seat(1, "ord_1", 1)
	if _, err := f.MoveOrSwap(1, 9); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing dst: want ErrNotFound, got %v", err)
	}
}
