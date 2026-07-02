package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/kitchen/internal/domain"
)

func ts() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

func mustTicket(t *testing.T, table string, items ...domain.NewItemInput) domain.Ticket {
	t.Helper()
	tk, err := domain.NewTicket("ord_x", table, items, ts())
	if err != nil {
		t.Fatalf("NewTicket: %v", err)
	}
	return tk
}

func TestNewTicket(t *testing.T) {
	tests := []struct {
		name    string
		orderID string
		table   string
		items   []domain.NewItemInput
		wantErr bool
	}{
		{"ok", "ord_1", "T7", []domain.NewItemInput{{Name: "Paneer Tikka", Station: "tandoor"}}, false},
		{"missing order", "", "T7", []domain.NewItemInput{{Name: "X"}}, true},
		{"missing table", "ord_1", "", []domain.NewItemInput{{Name: "X"}}, true},
		{"no items", "ord_1", "T7", nil, true},
		{"item missing name", "ord_1", "T7", []domain.NewItemInput{{Name: ""}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tk, err := domain.NewTicket(tc.orderID, tc.table, tc.items, ts())
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixTicket, tk.ID) {
				t.Fatalf("ticket id %q not a valid tkt_ ULID", tk.ID)
			}
			for _, it := range tk.Items {
				if it.State != domain.StateNew {
					t.Fatalf("new item should start at StateNew, got %d", it.State)
				}
			}
		})
	}
}

func TestNewTicket_UnknownStationFallsBackToGrill(t *testing.T) {
	tk := mustTicket(t, "T1", domain.NewItemInput{Name: "Soup", Station: "bogus"})
	if tk.Items[0].Station != "grill" {
		t.Fatalf("unknown station should fall back to grill, got %q", tk.Items[0].Station)
	}
}

func TestAdvanceItem_StateMachine(t *testing.T) {
	tests := []struct {
		name      string
		advances  int
		wantState domain.ItemState
	}{
		{"new", 0, domain.StateNew},
		{"prep", 1, domain.StatePrep},
		{"ready", 2, domain.StateReady},
		{"capped at ready", 5, domain.StateReady},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tk := mustTicket(t, "T1", domain.NewItemInput{Name: "Curry", Station: "grill"})
			for i := 0; i < tc.advances; i++ {
				if err := tk.AdvanceItem(0); err != nil {
					t.Fatalf("advance: %v", err)
				}
			}
			if tk.Items[0].State != tc.wantState {
				t.Fatalf("state = %d, want %d", tk.Items[0].State, tc.wantState)
			}
		})
	}
}

func TestAdvanceItem_OutOfRange(t *testing.T) {
	tk := mustTicket(t, "T1", domain.NewItemInput{Name: "X"})
	if err := tk.AdvanceItem(9); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid for bad index, got %v", err)
	}
}

func TestBumpAll_MarksEveryItemReady(t *testing.T) {
	tk := mustTicket(t, "T1",
		domain.NewItemInput{Name: "A", Station: "grill"},
		domain.NewItemInput{Name: "B", Station: "tandoor"},
		domain.NewItemInput{Name: "C", Station: "cold"},
	)
	if tk.IsAllReady() {
		t.Fatal("fresh ticket should not be all-ready")
	}
	tk.BumpAll()
	if !tk.IsAllReady() {
		t.Fatal("after BumpAll every item should be ready")
	}
	for _, it := range tk.Items {
		if it.State != domain.StateReady {
			t.Fatalf("item %s not ready after bump", it.Name)
		}
	}
}

func TestPhase(t *testing.T) {
	cooking := mustTicket(t, "T1", domain.NewItemInput{Name: "A"}, domain.NewItemInput{Name: "B"})
	if cooking.Phase() != domain.PhaseCooking {
		t.Fatalf("want cooking, got %s", cooking.Phase())
	}
	cooking.AdvanceItem(0) // only first item ready -> still cooking
	cooking.AdvanceItem(0)
	if cooking.Phase() != domain.PhaseCooking {
		t.Fatalf("partial ready should stay cooking, got %s", cooking.Phase())
	}

	ready := mustTicket(t, "T1", domain.NewItemInput{Name: "A"})
	ready.BumpAll()
	if ready.Phase() != domain.PhaseReady {
		t.Fatalf("want ready, got %s", ready.Phase())
	}

	served := mustTicket(t, "T1", domain.NewItemInput{Name: "A"})
	served.BumpAll()
	served.MarkServed()
	if served.Phase() != domain.PhaseServed {
		t.Fatalf("want served, got %s", served.Phase())
	}
}

func TestCookingBoard_ExcludesReadyAndServed(t *testing.T) {
	cooking := mustTicket(t, "T1", domain.NewItemInput{Name: "Cooking"})
	ready := mustTicket(t, "T2", domain.NewItemInput{Name: "Ready"})
	ready.BumpAll()
	served := mustTicket(t, "T3", domain.NewItemInput{Name: "Served"})
	served.BumpAll()
	served.MarkServed()

	board := domain.CookingBoard([]domain.Ticket{ready, served, cooking})
	if len(board) != 1 {
		t.Fatalf("board should have only the cooking ticket, got %d", len(board))
	}
	if board[0].Items[0].Name != "Cooking" {
		t.Fatalf("wrong ticket on board: %s", board[0].Items[0].Name)
	}
}

func TestServeQueue_ReadyAndUnserved(t *testing.T) {
	cooking := mustTicket(t, "T1", domain.NewItemInput{Name: "Cooking"})
	ready := mustTicket(t, "T2", domain.NewItemInput{Name: "Ready"})
	ready.BumpAll()
	served := mustTicket(t, "T3", domain.NewItemInput{Name: "Served"})
	served.BumpAll()
	served.MarkServed()

	q := domain.ServeQueue([]domain.Ticket{cooking, served, ready})
	if len(q) != 1 {
		t.Fatalf("serve queue should hold only the ready-unserved ticket, got %d", len(q))
	}
	if q[0].Items[0].Name != "Ready" {
		t.Fatalf("wrong ticket in queue: %s", q[0].Items[0].Name)
	}
}

func TestAllDayCounts(t *testing.T) {
	t1 := mustTicket(t, "T1", domain.NewItemInput{Name: "Naan"}, domain.NewItemInput{Name: "Dal"})
	t2 := mustTicket(t, "T2", domain.NewItemInput{Name: "Naan"})
	t2.AdvanceItem(0)
	t2.AdvanceItem(0) // Naan on t2 ready -> not counted
	served := mustTicket(t, "T3", domain.NewItemInput{Name: "Naan"})
	served.MarkServed() // served ticket excluded entirely

	counts := domain.AllDayCounts([]domain.Ticket{t1, t2, served})
	if counts["Naan"] != 1 {
		t.Fatalf("want 1 outstanding Naan, got %d", counts["Naan"])
	}
	if counts["Dal"] != 1 {
		t.Fatalf("want 1 outstanding Dal, got %d", counts["Dal"])
	}
}
