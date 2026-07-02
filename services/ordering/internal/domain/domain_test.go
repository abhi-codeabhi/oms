package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/ordering/internal/domain"
)

var now = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

func line(menuItem string, qty int32, minor int64) domain.NewLineInput {
	return domain.NewLineInput{MenuItemID: menuItem, Qty: qty, UnitPrice: money.New(minor, "INR")}
}

func TestNewOrder_ComputesSubtotal(t *testing.T) {
	tests := []struct {
		name      string
		items     []domain.NewLineInput
		wantMinor int64
		wantErr   bool
	}{
		{"single line", []domain.NewLineInput{line("itm_a", 2, 12000)}, 24000, false},
		{"multi line", []domain.NewLineInput{line("itm_a", 2, 12000), line("itm_b", 1, 5000)}, 29000, false},
		{"qty multiplies", []domain.NewLineInput{line("itm_a", 3, 10000)}, 30000, false},
		{"no items", nil, 0, true},
		{"zero qty", []domain.NewLineInput{line("itm_a", 0, 100)}, 0, true},
		{"missing menu item", []domain.NewLineInput{{Qty: 1, UnitPrice: money.New(100, "INR")}}, 0, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := domain.NewOrder("out_1", "T7", tc.items, now)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixOrder, o.ID) {
				t.Fatalf("order id %q invalid", o.ID)
			}
			if o.Subtotal.Minor != tc.wantMinor {
				t.Fatalf("subtotal = %d, want %d", o.Subtotal.Minor, tc.wantMinor)
			}
			if o.Subtotal.Currency != "INR" {
				t.Fatalf("currency = %q, want INR", o.Subtotal.Currency)
			}
			if o.Billed {
				t.Fatal("new order should not be billed")
			}
			for _, l := range o.Lines {
				if !ids.Valid(domain.PrefixLine, l.ID) {
					t.Fatalf("line id %q invalid", l.ID)
				}
			}
		})
	}
}

func TestNewOrder_NameDefaultsToMenuItemID(t *testing.T) {
	o, err := domain.NewOrder("out_1", "T1", []domain.NewLineInput{line("itm_paneer", 1, 100)}, now)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if o.Lines[0].Name != "itm_paneer" {
		t.Fatalf("name = %q, want fallback to menu item id", o.Lines[0].Name)
	}
}

func TestNewOrder_RequiresRestaurantAndTable(t *testing.T) {
	if _, err := domain.NewOrder("", "T1", []domain.NewLineInput{line("a", 1, 1)}, now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing restaurant should fail: %v", err)
	}
	if _, err := domain.NewOrder("out_1", "  ", []domain.NewLineInput{line("a", 1, 1)}, now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing table should fail: %v", err)
	}
}

func TestTableKey_TolerantMatching(t *testing.T) {
	tests := []struct{ in, want string }{
		{"T7", "7"},
		{"7", "7"},
		{"t07", "07"},
		{"Table 12", "12"},
		{"PATIO", "PATIO"},
		{"  T3  ", "3"},
	}
	for _, tc := range tests {
		if got := domain.TableKey(tc.in); got != tc.want {
			t.Fatalf("TableKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	// The whole point: "T7", "7" and "07"-less variants collide on the digit run.
	if domain.TableKey("T7") != domain.TableKey("7") {
		t.Fatal("T7 and 7 must match")
	}
}

func TestMarkBilledAndRelocate(t *testing.T) {
	o, _ := domain.NewOrder("out_1", "T5", []domain.NewLineInput{line("a", 1, 100)}, now)
	o.MarkBilled()
	if !o.Billed {
		t.Fatal("expected billed")
	}
	if err := o.Relocate("T9"); err != nil {
		t.Fatalf("relocate: %v", err)
	}
	if o.TableID != "T9" {
		t.Fatalf("table = %q, want T9", o.TableID)
	}
	if err := o.Relocate("  "); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty relocate target should fail: %v", err)
	}
}

func TestLineTotal(t *testing.T) {
	l := domain.Line{Qty: 4, UnitPrice: money.New(2500, "INR")}
	if got := l.LineTotal(); got.Minor != 10000 {
		t.Fatalf("line total = %d, want 10000", got.Minor)
	}
}
