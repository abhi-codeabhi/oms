package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/app"
	"github.com/restorna/platform/services/billing/internal/domain"
	"github.com/restorna/platform/services/billing/internal/ports"
)

const rid = "out_01hx0000000000000000000000"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func inr(minor int64) money.Money { return money.New(minor, "INR") }

func ctx() context.Context { return context.Background() }

// --- OpenForTable: aggregate multiple orders into one categorized bill ---

func TestOpenForTable_AggregatesOrdersResolvesNamesAndCategories(t *testing.T) {
	repo := newFakeRepo()
	orders := newFakeOrders()
	menu := newFakeMenu()
	menu.items["item_paneer"] = ports.ResolvedItem{Name: "Paneer Tikka", Category: "Appetizers"}
	menu.items["item_naan"] = ports.ResolvedItem{Name: "Butter Naan", Category: "Breads"}
	menu.items["item_dal"] = ports.ResolvedItem{Name: "Dal Makhani", Category: "Mains"}
	// Two orders at the same table; second adds another naan (qty 2).
	orders.orders["T7"] = []ports.Order{
		{ID: "ord_1", Table: "T7", Lines: []ports.OrderLine{
			{MenuItemID: "item_paneer", Qty: 1, UnitPrice: inr(22000)},
			{MenuItemID: "item_dal", Qty: 1, UnitPrice: inr(28000)},
		}},
		{ID: "ord_2", Table: "T7", Lines: []ports.OrderLine{
			{MenuItemID: "item_naan", Qty: 2, UnitPrice: inr(5000)},
		}},
	}
	settings := newFakeSettings(5, 0, domain.RoundNone) // GST 5%
	a := app.New(repo, orders, menu, settings, newFakePromos(), fixedClock())

	res, err := a.OpenForTable(ctx(), rid, "T7")
	if err != nil {
		t.Fatalf("OpenForTable: %v", err)
	}
	if res.OrderCount != 2 {
		t.Fatalf("order count = %d, want 2", res.OrderCount)
	}
	// 1 paneer + 1 dal + 2 naan = 4 lines (qty expanded).
	if len(res.Bill.Lines) != 4 {
		t.Fatalf("want 4 bill lines, got %d", len(res.Bill.Lines))
	}
	// subtotal = 220 + 280 + 2*50 = 600.00; GST 5% = 30.00; total = 630.00.
	if res.Totals.Subtotal.Minor != 60000 {
		t.Fatalf("subtotal = %d, want 60000", res.Totals.Subtotal.Minor)
	}
	if res.Totals.Tax.Minor != 3000 {
		t.Fatalf("tax = %d, want 3000", res.Totals.Tax.Minor)
	}
	if res.Totals.Total.Minor != 63000 {
		t.Fatalf("total = %d, want 63000", res.Totals.Total.Minor)
	}
	// names resolved from catalog.
	got := map[string]string{}
	for _, l := range res.Bill.Lines {
		got[l.Name] = l.Category
	}
	if got["Paneer Tikka"] != "Appetizers" || got["Butter Naan"] != "Breads" || got["Dal Makhani"] != "Mains" {
		t.Fatalf("names/categories not resolved: %+v", got)
	}
	// both orders marked billed.
	if len(orders.billed) != 2 {
		t.Fatalf("want 2 orders marked billed, got %d (%v)", len(orders.billed), orders.billed)
	}
	// bill.opened emitted once.
	if n := countEvents(repo, app.EventBillOpened); n != 1 {
		t.Fatalf("want 1 bill.opened event, got %d", n)
	}
	// persisted + unpaid.
	open, _ := repo.ListOpenBills(ctx(), rid)
	if len(open) != 1 {
		t.Fatalf("want 1 persisted open bill, got %d", len(open))
	}
}

func TestOpenForTable_SectionsGroupedAndOrdered(t *testing.T) {
	repo := newFakeRepo()
	orders := newFakeOrders()
	menu := newFakeMenu()
	menu.items["i_app"] = ports.ResolvedItem{Name: "Samosa", Category: "Appetizers"}
	menu.items["i_dess"] = ports.ResolvedItem{Name: "Kheer", Category: "Desserts"}
	menu.items["i_bread"] = ports.ResolvedItem{Name: "Naan", Category: "Breads"}
	orders.orders["T1"] = []ports.Order{{ID: "ord_1", Table: "T1", Lines: []ports.OrderLine{
		{MenuItemID: "i_dess", Qty: 1, UnitPrice: inr(12000)},
		{MenuItemID: "i_app", Qty: 1, UnitPrice: inr(8000)},
		{MenuItemID: "i_bread", Qty: 2, UnitPrice: inr(5000)},
	}}}
	a := app.New(repo, orders, menu, newFakeSettings(5, 0, domain.RoundNone), nil, fixedClock())

	res, err := a.OpenForTable(ctx(), rid, "T1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Appetizers", "Breads", "Desserts"}
	if len(res.Sections) != 3 {
		t.Fatalf("want 3 sections, got %d", len(res.Sections))
	}
	for i, w := range want {
		if res.Sections[i].Category != w {
			t.Fatalf("section[%d]=%s, want %s (menu order)", i, res.Sections[i].Category, w)
		}
	}
	for _, s := range res.Sections {
		if s.Category == "Breads" && (s.Count != 2 || s.Subtotal.Minor != 10000) {
			t.Fatalf("breads section count=%d subtotal=%d, want 2/10000", s.Count, s.Subtotal.Minor)
		}
	}
}

func TestOpenForTable_NoOrders(t *testing.T) {
	a := app.New(newFakeRepo(), newFakeOrders(), newFakeMenu(), newFakeSettings(5, 0, domain.RoundNone), nil, fixedClock())
	_, err := a.OpenForTable(ctx(), rid, "T9")
	if err == nil {
		t.Fatal("want error when no open orders to bill")
	}
}

// --- ApplyDiscount: flat amount + coupon both lower the total ---

func openOneBill(t *testing.T, a *app.App, repo *fakeRepo) string {
	t.Helper()
	res, err := a.OpenForTable(ctx(), rid, "T1")
	if err != nil {
		t.Fatalf("seed OpenForTable: %v", err)
	}
	_ = repo
	return res.Bill.ID
}

func seedSingleItemTable(orders *fakeOrders, menu *fakeMenu) {
	menu.items["i_main"] = ports.ResolvedItem{Name: "Biryani", Category: "Mains"}
	orders.orders["T1"] = []ports.Order{{ID: "ord_1", Table: "T1", Lines: []ports.OrderLine{
		{MenuItemID: "i_main", Qty: 1, UnitPrice: inr(40000)},
	}}}
}

func TestApplyDiscount_FlatAmountLowersTotal(t *testing.T) {
	repo := newFakeRepo()
	orders := newFakeOrders()
	menu := newFakeMenu()
	seedSingleItemTable(orders, menu)
	a := app.New(repo, orders, menu, newFakeSettings(5, 0, domain.RoundNone), newFakePromos(), fixedClock())
	billID := openOneBill(t, a, repo)

	before, _ := a.GetBill(ctx(), rid, billID)
	v, err := a.ApplyDiscount(ctx(), rid, app.ApplyDiscountInput{BillID: billID, AmountMinor: 10000, Reason: "loyalty"})
	if err != nil {
		t.Fatalf("ApplyDiscount: %v", err)
	}
	if v.Totals.Total.Minor >= before.Totals.Total.Minor {
		t.Fatalf("discount must lower total (%d -> %d)", before.Totals.Total.Minor, v.Totals.Total.Minor)
	}
	// subtotal 400, -100 discount = 300 taxable, +5% = 315.00.
	if v.Totals.Total.Minor != 31500 {
		t.Fatalf("discounted total = %d, want 31500", v.Totals.Total.Minor)
	}
	if n := countEvents(repo, app.EventDiscountApplied); n != 1 {
		t.Fatalf("want 1 discount_applied event, got %d", n)
	}
}

func TestApplyDiscount_CouponViaPromotions(t *testing.T) {
	repo := newFakeRepo()
	orders := newFakeOrders()
	menu := newFakeMenu()
	seedSingleItemTable(orders, menu)
	promos := newFakePromos()
	promos.discounts["SAVE50"] = 5000 // 50.00 off
	a := app.New(repo, orders, menu, newFakeSettings(5, 0, domain.RoundNone), promos, fixedClock())
	billID := openOneBill(t, a, repo)

	v, err := a.ApplyDiscount(ctx(), rid, app.ApplyDiscountInput{BillID: billID, CouponCode: "SAVE50"})
	if err != nil {
		t.Fatalf("ApplyDiscount coupon: %v", err)
	}
	if v.Totals.Discount.Minor != 5000 {
		t.Fatalf("coupon discount = %d, want 5000", v.Totals.Discount.Minor)
	}
	// subtotal 400, -50 = 350 taxable, +5% = 367.50.
	if v.Totals.Total.Minor != 36750 {
		t.Fatalf("coupon total = %d, want 36750", v.Totals.Total.Minor)
	}
}

// --- TakePayment: finalizes + emits ---

func TestTakePayment_FinalizesAndEmits(t *testing.T) {
	repo := newFakeRepo()
	orders := newFakeOrders()
	menu := newFakeMenu()
	seedSingleItemTable(orders, menu) // subtotal 400, GST 0 below
	a := app.New(repo, orders, menu, newFakeSettings(0, 0, domain.RoundNone), nil, fixedClock())
	billID := openOneBill(t, a, repo)

	// total = 400.00. Partial first.
	r1, err := a.TakePayment(ctx(), rid, billID, "cash", 15000, "")
	if err != nil {
		t.Fatalf("TakePayment partial: %v", err)
	}
	if r1.Paid {
		t.Fatal("partial payment should not finalize")
	}
	if n := countEvents(repo, app.EventBillFinalized); n != 0 {
		t.Fatalf("no finalize event yet, got %d", n)
	}
	// Cover the rest.
	r2, err := a.TakePayment(ctx(), rid, billID, "upi", 25000, "txn123")
	if err != nil {
		t.Fatalf("TakePayment final: %v", err)
	}
	if !r2.Paid {
		t.Fatal("covering payment should finalize the bill")
	}
	if n := countEvents(repo, app.EventPaymentCaptured); n != 2 {
		t.Fatalf("want 2 payment.captured events, got %d", n)
	}
	if n := countEvents(repo, app.EventBillFinalized); n != 1 {
		t.Fatalf("want 1 bill.finalized event, got %d", n)
	}
	// no longer in the open queue.
	open, _ := repo.ListOpenBills(ctx(), rid)
	if len(open) != 0 {
		t.Fatalf("paid bill should leave the open queue, got %d", len(open))
	}
}

// --- OpenTabs projection: order placed -> running total -> bill_ready -> removed ---

func TestOpenTabsProjection_OrderPlacedAddsRunningTotal(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock())

	if err := a.OnOrderPlaced(ctx(), app.OrderPlaced{
		EventID: "evt_1", RestaurantID: rid, OrderID: "ord_1", Table: "T7",
		ItemUnits: 2, SubtotalMinor: 30000, Currency: "INR",
	}); err != nil {
		t.Fatalf("OnOrderPlaced: %v", err)
	}
	if err := a.OnOrderPlaced(ctx(), app.OrderPlaced{
		EventID: "evt_2", RestaurantID: rid, OrderID: "ord_2", Table: "T7",
		ItemUnits: 1, SubtotalMinor: 15000, Currency: "INR",
	}); err != nil {
		t.Fatalf("OnOrderPlaced 2: %v", err)
	}
	tabs, _ := a.OpenTabs(ctx(), rid)
	if len(tabs) != 1 {
		t.Fatalf("want 1 tab, got %d", len(tabs))
	}
	tab := tabs[0]
	if tab.Table != 7 || tab.OrderCount != 2 || tab.ItemCount != 3 || tab.Running.Minor != 45000 {
		t.Fatalf("tab projection wrong: %+v", tab)
	}
	if tab.Status() != domain.StatusOpen {
		t.Fatalf("status = %s, want open", tab.Status())
	}
}

func TestOpenTabsProjection_Idempotent(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock())
	ev := app.OrderPlaced{EventID: "evt_dup", RestaurantID: rid, OrderID: "ord_1", Table: "T3", ItemUnits: 1, SubtotalMinor: 10000, Currency: "INR"}
	if err := a.OnOrderPlaced(ctx(), ev); err != nil {
		t.Fatal(err)
	}
	// Redelivery must be a no-op (running total not doubled).
	if err := a.OnOrderPlaced(ctx(), ev); err != nil {
		t.Fatal(err)
	}
	tabs, _ := a.OpenTabs(ctx(), rid)
	if len(tabs) != 1 || tabs[0].OrderCount != 1 || tabs[0].Running.Minor != 10000 {
		t.Fatalf("redelivery should be a no-op: %+v", tabs)
	}
}

func TestOpenTabsProjection_AskedThenBillReadyThenFinalizedRemoves(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, nil, nil, nil, nil, fixedClock())
	// order places the tab.
	a.OnOrderPlaced(ctx(), app.OrderPlaced{EventID: "e1", RestaurantID: rid, OrderID: "ord_1", Table: "T5", ItemUnits: 2, SubtotalMinor: 50000, Currency: "INR"})

	// bill requested -> asked.
	if err := a.OnBillAsked(ctx(), app.BillAsked{EventID: "e2", RestaurantID: rid, Table: "T5"}); err != nil {
		t.Fatal(err)
	}
	tabs, _ := a.OpenTabs(ctx(), rid)
	if tabs[0].Status() != domain.StatusAsked {
		t.Fatalf("status after ask = %s, want asked", tabs[0].Status())
	}

	// bill opened -> bill_ready (beats asked).
	if err := a.OnBillOpened(ctx(), app.BillOpened{EventID: "e3", RestaurantID: rid, BillID: "bill_1", Table: "T5", TotalMinor: 52500, Currency: "INR"}); err != nil {
		t.Fatal(err)
	}
	tabs, _ = a.OpenTabs(ctx(), rid)
	if tabs[0].Status() != domain.StatusBillReady {
		t.Fatalf("status after bill opened = %s, want bill_ready", tabs[0].Status())
	}
	if tabs[0].BillID != "bill_1" || tabs[0].BillTotal.Minor != 52500 {
		t.Fatalf("bill not attached: %+v", tabs[0])
	}

	// bill finalized -> tab removed.
	if err := a.OnBillFinalized(ctx(), app.BillFinalized{EventID: "e4", RestaurantID: rid, BillID: "bill_1", Table: "T5"}); err != nil {
		t.Fatal(err)
	}
	tabs, _ = a.OpenTabs(ctx(), rid)
	if len(tabs) != 0 {
		t.Fatalf("finalized table should leave the board, got %d tabs", len(tabs))
	}
}

func TestOpenForTable_RequiresClients(t *testing.T) {
	// Projection-only wiring (nil clients) must reject OpenForTable cleanly.
	a := app.New(newFakeRepo(), nil, nil, nil, nil, fixedClock())
	if _, err := a.OpenForTable(ctx(), rid, "T1"); err == nil {
		t.Fatal("want error when ordering/catalog/settings clients are nil")
	}
}
