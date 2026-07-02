package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/domain"
)

func clk() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

func inr(minor int64) money.Money { return money.New(minor, "INR") }

func lines(specs ...struct {
	name string
	cat  string
	px   int64
}) []domain.NewLineInput {
	out := make([]domain.NewLineInput, 0, len(specs))
	for _, s := range specs {
		out = append(out, domain.NewLineInput{Name: s.name, Category: s.cat, Price: inr(s.px)})
	}
	return out
}

func TestNewBill_Validation(t *testing.T) {
	cases := []struct {
		name   string
		rest   string
		table  string
		lines  []domain.NewLineInput
		wantOK bool
	}{
		{"ok", "out_1", "T7", lines(struct {
			name string
			cat  string
			px   int64
		}{"Naan", "Breads", 5000}), true},
		{"no restaurant", "", "T7", lines(struct {
			name string
			cat  string
			px   int64
		}{"Naan", "Breads", 5000}), false},
		{"no table", "out_1", "", lines(struct {
			name string
			cat  string
			px   int64
		}{"Naan", "Breads", 5000}), false},
		{"no lines", "out_1", "T7", nil, false},
		{"blank name", "out_1", "T7", []domain.NewLineInput{{Name: "  ", Price: inr(100)}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := domain.NewBill(c.rest, c.table, []string{"ord_1"}, c.lines, clk())
			if c.wantOK && err != nil {
				t.Fatalf("want ok, got %v", err)
			}
			if !c.wantOK {
				if err == nil {
					t.Fatal("want error")
				}
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %v", err)
				}
			}
		})
	}
}

func TestComputeTotals_GSTAndServiceCharge(t *testing.T) {
	// subtotal 400.00, GST 5% = 20.00, service charge 10% = 40.00 -> total 460.00.
	b, err := domain.NewBill("out_1", "T1", []string{"ord_1"},
		lines(struct {
			name string
			cat  string
			px   int64
		}{"Mains", "Mains", 40000}), clk())
	if err != nil {
		t.Fatal(err)
	}
	tot := b.ComputeTotals(domain.TaxConfig{GSTPct: 5, ServiceChargePct: 10, Currency: "INR"})
	if tot.Subtotal.Minor != 40000 {
		t.Fatalf("subtotal = %d, want 40000", tot.Subtotal.Minor)
	}
	if tot.Tax.Minor != 2000 {
		t.Fatalf("tax = %d, want 2000", tot.Tax.Minor)
	}
	if tot.ServiceCharge.Minor != 4000 {
		t.Fatalf("service charge = %d, want 4000", tot.ServiceCharge.Minor)
	}
	if tot.Total.Minor != 46000 {
		t.Fatalf("total = %d, want 46000", tot.Total.Minor)
	}
}

func TestComputeTotals_DiscountLowersTaxableAndTotal(t *testing.T) {
	b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"},
		lines(struct {
			name string
			cat  string
			px   int64
		}{"Mains", "Mains", 40000}), clk())
	cfg := domain.TaxConfig{GSTPct: 5, Currency: "INR"}
	before := b.ComputeTotals(cfg).Total.Minor
	if err := b.ApplyDiscount(10000); err != nil { // 100.00 off
		t.Fatal(err)
	}
	after := b.ComputeTotals(cfg)
	// taxable = 300.00, tax 5% = 15.00, total = 315.00.
	if after.Total.Minor != 31500 {
		t.Fatalf("discounted total = %d, want 31500", after.Total.Minor)
	}
	if after.Total.Minor >= before {
		t.Fatalf("discount must lower total (%d -> %d)", before, after.Total.Minor)
	}
	if after.Discount.Minor != 10000 {
		t.Fatalf("discount = %d, want 10000", after.Discount.Minor)
	}
}

func TestComputeTotals_DiscountClampedToSubtotal(t *testing.T) {
	b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"},
		lines(struct {
			name string
			cat  string
			px   int64
		}{"X", "Mains", 5000}), clk())
	b.ApplyDiscount(99999) // more than subtotal
	tot := b.ComputeTotals(domain.TaxConfig{GSTPct: 5, Currency: "INR"})
	if tot.Total.Minor != 0 {
		t.Fatalf("over-discount should floor total at 0, got %d", tot.Total.Minor)
	}
}

func TestRounding(t *testing.T) {
	// subtotal 100.49, GST 0 -> total 100.49; nearest_1 -> 100.00, up_1 -> 101.00.
	mk := func() domain.Bill {
		b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"},
			lines(struct {
				name string
				cat  string
				px   int64
			}{"X", "Mains", 10049}), clk())
		return b
	}
	cases := []struct {
		mode domain.Rounding
		want int64
	}{
		{domain.RoundNone, 10049},
		{domain.RoundNearest1, 10000},
		{domain.RoundUp1, 10100},
		{domain.RoundDown1, 10000},
	}
	for _, c := range cases {
		b := mk()
		got := b.ComputeTotals(domain.TaxConfig{GSTPct: 0, Rounding: c.mode, Currency: "INR"}).Total.Minor
		if got != c.want {
			t.Fatalf("rounding %s: total = %d, want %d", c.mode, got, c.want)
		}
	}
}

func TestRecordPayment_FinalizesOnFullCover(t *testing.T) {
	b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"},
		lines(struct {
			name string
			cat  string
			px   int64
		}{"X", "Mains", 10000}), clk())
	cfg := domain.TaxConfig{GSTPct: 0, Currency: "INR"} // total = 100.00
	if _, err := b.RecordPayment("cash", 6000, "", cfg, clk()); err != nil {
		t.Fatal(err)
	}
	if b.Paid {
		t.Fatal("partial payment should not finalize")
	}
	if _, err := b.RecordPayment("upi", 4000, "txn", cfg, clk()); err != nil {
		t.Fatal(err)
	}
	if !b.Paid {
		t.Fatal("covering payments should finalize the bill")
	}
	if len(b.Payments) != 2 {
		t.Fatalf("want 2 payments, got %d", len(b.Payments))
	}
}

func TestRecordPayment_Validation(t *testing.T) {
	b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"},
		lines(struct {
			name string
			cat  string
			px   int64
		}{"X", "Mains", 10000}), clk())
	cfg := domain.TaxConfig{Currency: "INR"}
	if _, err := b.RecordPayment("bitcoin", 100, "", cfg, clk()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unknown method: want ErrInvalid, got %v", err)
	}
	if _, err := b.RecordPayment("cash", 0, "", cfg, clk()); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("zero amount: want ErrInvalid, got %v", err)
	}
}

func TestSections_GroupedAndOrdered(t *testing.T) {
	b, _ := domain.NewBill("out_1", "T1", []string{"ord_1"}, lines(
		struct {
			name string
			cat  string
			px   int64
		}{"Gulab Jamun", "Desserts", 12000},
		struct {
			name string
			cat  string
			px   int64
		}{"Paneer Tikka", "Appetizers", 22000},
		struct {
			name string
			cat  string
			px   int64
		}{"Butter Naan", "Breads", 5000},
		struct {
			name string
			cat  string
			px   int64
		}{"Garlic Naan", "Breads", 6000},
		struct {
			name string
			cat  string
			px   int64
		}{"Mystery", "Sauce", 1000}, // unknown -> sorts last
	), clk())
	secs := b.Sections()
	if len(secs) != 4 {
		t.Fatalf("want 4 sections, got %d", len(secs))
	}
	// Appetizers first, Breads next, Desserts, then unknown last.
	wantOrder := []string{"Appetizers", "Breads", "Desserts", "Sauce"}
	for i, w := range wantOrder {
		if secs[i].Category != w {
			t.Fatalf("section[%d] = %s, want %s", i, secs[i].Category, w)
		}
	}
	// Breads aggregates two lines: count 2, subtotal 110.00.
	for _, s := range secs {
		if s.Category == "Breads" {
			if s.Count != 2 || s.Subtotal.Minor != 11000 {
				t.Fatalf("breads section: count=%d subtotal=%d, want 2/11000", s.Count, s.Subtotal.Minor)
			}
		}
	}
}

func TestTableNumber(t *testing.T) {
	cases := map[string]int32{"T7": 7, "7": 7, "table 12": 12, "abc": 0, "": 0}
	for in, want := range cases {
		if got := domain.TableNumber(in); got != want {
			t.Fatalf("TableNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestTab_StatusPrecedence(t *testing.T) {
	tab := domain.Tab{Table: 3}
	if tab.Status() != domain.StatusOpen {
		t.Fatalf("fresh tab status = %s, want open", tab.Status())
	}
	tab.MarkAsked()
	if tab.Status() != domain.StatusAsked {
		t.Fatalf("asked tab status = %s, want asked", tab.Status())
	}
	tab.AttachBill("bill_1", inr(50000))
	if tab.Status() != domain.StatusBillReady {
		t.Fatalf("billed tab status = %s, want bill_ready (bill beats asked)", tab.Status())
	}
}

func TestTab_AddOrderAccumulates(t *testing.T) {
	var tab domain.Tab
	tab.AddOrder(2, 30000, "INR")
	tab.AddOrder(1, 15000, "INR")
	if tab.OrderCount != 2 {
		t.Fatalf("order count = %d, want 2", tab.OrderCount)
	}
	if tab.ItemCount != 3 {
		t.Fatalf("item count = %d, want 3", tab.ItemCount)
	}
	if tab.Running.Minor != 45000 {
		t.Fatalf("running = %d, want 45000", tab.Running.Minor)
	}
}
