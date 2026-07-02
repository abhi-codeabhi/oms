// Package app holds the billing use cases. It depends only on ports + domain. It
// orchestrates the OpenForTable flow (ordering -> catalog -> settings -> persist
// -> markBilled -> emit), discount (promotions/flat), payment capture/finalize,
// and the event-driven Tab read model. The grpc adapter maps proto <-> these
// calls; nats consumers drive the projection handlers; tests use in-memory fakes.
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/domain"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Event types emitted by this service (CONVENTIONS.md naming:
// restorna.<context>.<aggregate>.<event>.v1).
const (
	EventBillOpened       = "restorna.billing.bill.opened.v1"
	EventDiscountApplied  = "restorna.billing.bill.discount_applied.v1"
	EventPaymentCaptured  = "restorna.billing.payment.captured.v1"
	EventBillFinalized    = "restorna.billing.bill.finalized.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer). The
// outbound clients (orders/menu/settings/promos) may be nil for wirings that do
// not need them (the projection consumers only need repo); OpenForTable requires
// orders + menu + settings.
type App struct {
	repo     ports.Repository
	orders   ports.Orders
	menu     ports.Menu
	settings ports.Settings
	promos   ports.Promotions
	now      Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, orders ports.Orders, menu ports.Menu, settings ports.Settings, promos ports.Promotions, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, orders: orders, menu: menu, settings: settings, promos: promos, now: now}
}

// --- OpenForTable: aggregate every unbilled order into ONE categorized bill ---

// OpenForTableResult is the OpenForTable outcome: the persisted bill, its course
// sections, the computed totals, and how many orders were aggregated.
type OpenForTableResult struct {
	Bill       domain.Bill
	Totals     domain.Totals
	Sections   []domain.Section
	OrderCount int
}

// OpenForTable gathers EVERY unbilled order for the table (ordering), resolves
// each line's name + category (catalog), expands qty into per-unit bill lines,
// reads the effective tax config (settings), opens ONE aggregated bill, marks the
// contributing orders billed, and emits bill.opened. The Tab read model flips to
// bill_ready when it consumes bill.opened.
func (a *App) OpenForTable(ctx context.Context, restaurantID, table string) (OpenForTableResult, error) {
	if a.orders == nil || a.menu == nil || a.settings == nil {
		return OpenForTableResult{}, fmt.Errorf("%w: OpenForTable requires ordering+catalog+settings clients", domain.ErrInvalid)
	}
	restaurantID = strings.TrimSpace(restaurantID)
	table = strings.TrimSpace(table)
	if restaurantID == "" {
		return OpenForTableResult{}, fmt.Errorf("%w: restaurant_id is required", domain.ErrInvalid)
	}
	if table == "" {
		return OpenForTableResult{}, fmt.Errorf("%w: table is required", domain.ErrInvalid)
	}

	orders, err := a.orders.ListForTable(ctx, restaurantID, table)
	if err != nil {
		return OpenForTableResult{}, fmt.Errorf("list orders for table %s: %w", table, err)
	}
	if len(orders) == 0 {
		return OpenForTableResult{}, fmt.Errorf("%w: no open orders to bill for table %s", domain.ErrNotFound, table)
	}

	cfg, err := a.settings.BillingConfig(ctx, restaurantID)
	if err != nil {
		return OpenForTableResult{}, fmt.Errorf("read billing config: %w", err)
	}
	currency := cfg.Currency
	if currency == "" {
		currency = domain.DefaultCurrency
		cfg.Currency = currency
	}

	// Flatten every order's lines into per-unit bill lines, resolving name +
	// category from catalog (orders may carry only a menu_item_id). The catalog is
	// the source of truth for the course grouping.
	lines := make([]domain.NewLineInput, 0, 16)
	orderIDs := make([]string, 0, len(orders))
	for _, o := range orders {
		orderIDs = append(orderIDs, o.ID)
		for _, ln := range o.Lines {
			name := strings.TrimSpace(ln.Name)
			category := ""
			needResolve := name == "" || name == ln.MenuItemID
			if a.menu != nil && ln.MenuItemID != "" {
				if r, rerr := a.menu.GetItem(ctx, restaurantID, ln.MenuItemID); rerr == nil {
					if needResolve && strings.TrimSpace(r.Name) != "" {
						name = r.Name
					}
					if strings.TrimSpace(r.Category) != "" {
						category = r.Category
					}
				}
			}
			if name == "" {
				name = "Item"
			}
			if category == "" {
				category = domain.CategoryOther
			}
			price := ln.UnitPrice
			if price.Currency == "" {
				price.Currency = currency
			}
			qty := ln.Qty
			if qty < 1 {
				qty = 1
			}
			for i := int32(0); i < qty; i++ {
				lines = append(lines, domain.NewLineInput{Name: name, Category: category, Price: price})
			}
		}
	}

	bill, err := domain.NewBill(restaurantID, table, orderIDs, lines, a.now())
	if err != nil {
		return OpenForTableResult{}, err
	}
	bill.Currency = currency
	totals := bill.ComputeTotals(cfg)

	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if err := tx.InsertBill(ctx, bill); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventBillOpened, restaurantID, billOpenedEvent(bill, totals))
	})
	if err != nil {
		return OpenForTableResult{}, err
	}

	// Mark contributing orders billed so a second "ask for bill" won't re-bill.
	if err := a.orders.MarkBilled(ctx, restaurantID, orderIDs); err != nil {
		return OpenForTableResult{}, fmt.Errorf("mark orders billed: %w", err)
	}

	return OpenForTableResult{
		Bill:       bill,
		Totals:     totals,
		Sections:   bill.Sections(),
		OrderCount: len(orders),
	}, nil
}

// --- queries ---

// BillView pairs a bill with its computed totals + sections for the grpc adapter.
type BillView struct {
	Bill     domain.Bill
	Totals   domain.Totals
	Sections []domain.Section
}

// GetBill returns a bill with its computed totals + sections.
func (a *App) GetBill(ctx context.Context, restaurantID, billID string) (BillView, error) {
	bill, err := a.repo.GetBill(ctx, restaurantID, billID)
	if err != nil {
		return BillView{}, err
	}
	cfg, err := a.billingConfig(ctx, restaurantID, bill.Currency)
	if err != nil {
		return BillView{}, err
	}
	return BillView{Bill: bill, Totals: bill.ComputeTotals(cfg), Sections: bill.Sections()}, nil
}

// ListOpen returns the unpaid bills (the billing surface's queue) with totals.
func (a *App) ListOpen(ctx context.Context, restaurantID string) ([]BillView, error) {
	bills, err := a.repo.ListOpenBills(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	out := make([]BillView, 0, len(bills))
	for _, b := range bills {
		cfg, err := a.billingConfig(ctx, restaurantID, b.Currency)
		if err != nil {
			return nil, err
		}
		out = append(out, BillView{Bill: b, Totals: b.ComputeTotals(cfg), Sections: b.Sections()})
	}
	return out, nil
}

// --- ApplyDiscount: coupon (promotions) or flat amount; recompute totals ---

// ApplyDiscountInput carries either a coupon code (evaluated via promotions) or a
// flat amount in minor units. When CouponCode is set it wins; otherwise AmountMinor.
type ApplyDiscountInput struct {
	BillID      string
	CouponCode  string
	AmountMinor int64
	Reason      string
}

// ApplyDiscount lowers the bill total either by evaluating a coupon against the
// current subtotal (PromotionsService) or by a flat amount, then recomputes
// totals and emits discount_applied.
func (a *App) ApplyDiscount(ctx context.Context, restaurantID string, in ApplyDiscountInput) (BillView, error) {
	var out BillView
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		bill, err := tx.GetBill(ctx, in.BillID)
		if err != nil {
			return err
		}
		cfg, err := a.billingConfig(ctx, restaurantID, bill.Currency)
		if err != nil {
			return err
		}

		minor := in.AmountMinor
		reason := strings.TrimSpace(in.Reason)
		if code := strings.TrimSpace(in.CouponCode); code != "" {
			if a.promos == nil {
				return fmt.Errorf("%w: coupon discount requires promotions client", domain.ErrInvalid)
			}
			subtotal := bill.Subtotal()
			dmin, applied, perr := a.promos.Evaluate(ctx, restaurantID, code, subtotal)
			if perr != nil {
				return fmt.Errorf("evaluate coupon %s: %w", code, perr)
			}
			if dmin <= 0 {
				return fmt.Errorf("%w: coupon %s yields no discount", domain.ErrInvalid, code)
			}
			minor = dmin
			if reason == "" {
				reason = applied
			}
			if reason == "" {
				reason = code
			}
		}

		if err := bill.ApplyDiscount(minor); err != nil {
			return err
		}
		if err := tx.UpdateBill(ctx, bill); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventDiscountApplied, restaurantID, discountEvent(bill, minor, reason)); err != nil {
			return err
		}
		out = BillView{Bill: bill, Totals: bill.ComputeTotals(cfg), Sections: bill.Sections()}
		return nil
	})
	if err != nil {
		return BillView{}, err
	}
	return out, nil
}

// --- TakePayment: record a payment; finalize + emit when paid in full ---

// TakePaymentResult is the TakePayment outcome.
type TakePaymentResult struct {
	Bill   domain.Bill
	Totals domain.Totals
	Paid   bool
}

// TakePayment records a payment against the bill. payment.captured is emitted for
// every tender; when the payments cover the total the bill is finalized (paid),
// bill.finalized is emitted, and the Tab read model removes the table on consume.
func (a *App) TakePayment(ctx context.Context, restaurantID, billID, method string, amountMinor int64, ref string) (TakePaymentResult, error) {
	var out TakePaymentResult
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		bill, err := tx.GetBill(ctx, billID)
		if err != nil {
			return err
		}
		cfg, err := a.billingConfig(ctx, restaurantID, bill.Currency)
		if err != nil {
			return err
		}
		totals := bill.ComputeTotals(cfg)
		pay, err := bill.RecordPayment(method, amountMinor, ref, cfg, a.now())
		if err != nil {
			return err
		}
		if err := tx.UpdateBill(ctx, bill); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventPaymentCaptured, restaurantID, paymentEvent(bill, pay)); err != nil {
			return err
		}
		if bill.Paid {
			if err := tx.StageEvent(ctx, EventBillFinalized, restaurantID, finalizedEvent(bill, totals)); err != nil {
				return err
			}
		}
		out = TakePaymentResult{Bill: bill, Totals: totals, Paid: bill.Paid}
		return nil
	})
	if err != nil {
		return TakePaymentResult{}, err
	}
	return out, nil
}

// --- billing board (read model) query ---

// OpenTabs returns the live billing board (event-driven read model), sorted by
// table number. Each tab's status is bill_ready > asked > open.
func (a *App) OpenTabs(ctx context.Context, restaurantID string) ([]domain.Tab, error) {
	tabs, err := a.repo.ListTabs(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	domain.SortTabs(tabs)
	return tabs, nil
}

// --- event-driven projection handlers (driven by the nats consumers) ---

// OrderPlaced is the parsed ordering.order.placed.v1 payload the consumer hands
// in: it folds the order's running total + counts into the table's tab.
type OrderPlaced struct {
	EventID      string
	RestaurantID string
	OrderID      string
	Table        string
	ItemUnits    int32
	SubtotalMinor int64
	Currency     string
}

// OnOrderPlaced projects an order-placed event into the Tab read model: it adds
// the order's running total + order/item counts to the table's tab (creating it
// on the first order). Idempotent: a redelivered event id is a no-op.
func (a *App) OnOrderPlaced(ctx context.Context, ev OrderPlaced) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: order.placed missing restaurant_id", domain.ErrInvalid)
	}
	tableNum := domain.TableNumber(ev.Table)
	if tableNum == 0 {
		return nil // no numeric table -> nothing to board
	}
	currency := ev.Currency
	if currency == "" {
		currency = domain.DefaultCurrency
	}
	return a.repo.Atomic(ctx, ev.RestaurantID, func(tx ports.Tx) error {
		if seen, err := tx.Seen(ctx, ev.RestaurantID, ev.EventID); err != nil {
			return err
		} else if seen {
			return nil
		}
		tab, _, err := tx.GetTab(ctx, tableNum)
		if err != nil {
			return err
		}
		tab.RestaurantID = ev.RestaurantID
		tab.Table = tableNum
		tab.AddOrder(ev.ItemUnits, ev.SubtotalMinor, currency)
		if err := tx.UpsertTab(ctx, tab); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID)
	})
}

// BillAsked is the parsed servicerequests.raised.v1 (type=bill) payload.
type BillAsked struct {
	EventID      string
	RestaurantID string
	Table        string
}

// OnBillAsked marks the table's tab "asked" when a bill service request is
// raised. Idempotent on the event id. If no tab exists yet (no orders), one is
// created in the asked state so the board surfaces the request.
func (a *App) OnBillAsked(ctx context.Context, ev BillAsked) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: raised missing restaurant_id", domain.ErrInvalid)
	}
	tableNum := domain.TableNumber(ev.Table)
	if tableNum == 0 {
		return nil
	}
	return a.repo.Atomic(ctx, ev.RestaurantID, func(tx ports.Tx) error {
		if seen, err := tx.Seen(ctx, ev.RestaurantID, ev.EventID); err != nil {
			return err
		} else if seen {
			return nil
		}
		tab, _, err := tx.GetTab(ctx, tableNum)
		if err != nil {
			return err
		}
		tab.RestaurantID = ev.RestaurantID
		tab.Table = tableNum
		tab.MarkAsked()
		if err := tx.UpsertTab(ctx, tab); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID)
	})
}

// BillOpened is the parsed billing.bill.opened.v1 payload (this service's own
// event, consumed to drive its read model — keeps the projection event-sourced).
type BillOpened struct {
	EventID      string
	RestaurantID string
	BillID       string
	Table        string
	TotalMinor   int64
	Currency     string
}

// OnBillOpened flips the table's tab to bill_ready, attaching the opened bill's
// id + total. Idempotent on the event id.
func (a *App) OnBillOpened(ctx context.Context, ev BillOpened) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: bill.opened missing restaurant_id", domain.ErrInvalid)
	}
	tableNum := domain.TableNumber(ev.Table)
	if tableNum == 0 {
		return nil
	}
	currency := ev.Currency
	if currency == "" {
		currency = domain.DefaultCurrency
	}
	return a.repo.Atomic(ctx, ev.RestaurantID, func(tx ports.Tx) error {
		if seen, err := tx.Seen(ctx, ev.RestaurantID, ev.EventID); err != nil {
			return err
		} else if seen {
			return nil
		}
		tab, _, err := tx.GetTab(ctx, tableNum)
		if err != nil {
			return err
		}
		tab.RestaurantID = ev.RestaurantID
		tab.Table = tableNum
		tab.AttachBill(ev.BillID, money.New(ev.TotalMinor, currency))
		if err := tx.UpsertTab(ctx, tab); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID)
	})
}

// BillFinalized is the parsed billing.bill.finalized.v1 payload.
type BillFinalized struct {
	EventID      string
	RestaurantID string
	BillID       string
	Table        string
}

// OnBillFinalized removes the table's tab from the board once its bill is paid in
// full. Idempotent on the event id.
func (a *App) OnBillFinalized(ctx context.Context, ev BillFinalized) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: bill.finalized missing restaurant_id", domain.ErrInvalid)
	}
	tableNum := domain.TableNumber(ev.Table)
	if tableNum == 0 {
		return nil
	}
	return a.repo.Atomic(ctx, ev.RestaurantID, func(tx ports.Tx) error {
		if seen, err := tx.Seen(ctx, ev.RestaurantID, ev.EventID); err != nil {
			return err
		} else if seen {
			return nil
		}
		if err := tx.DeleteTab(ctx, tableNum); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID)
	})
}

// --- helpers ---

// billingConfig reads the effective tax config, falling back to a sane default
// (GST 5%, no service charge, no rounding) when no settings client is wired or
// the lookup fails — billing must never block on a config read. ccy pins the
// currency to the bill's own when settings omits it.
func (a *App) billingConfig(ctx context.Context, restaurantID, ccy string) (domain.TaxConfig, error) {
	cfg := domain.TaxConfig{GSTPct: 5, ServiceChargePct: 0, Rounding: domain.RoundNone, Currency: ccy}
	if a.settings == nil {
		if cfg.Currency == "" {
			cfg.Currency = domain.DefaultCurrency
		}
		return cfg, nil
	}
	resolved, err := a.settings.BillingConfig(ctx, restaurantID)
	if err != nil {
		// Tolerate a settings miss with the default so a settle never blocks.
		if cfg.Currency == "" {
			cfg.Currency = domain.DefaultCurrency
		}
		return cfg, nil
	}
	if resolved.Currency == "" {
		resolved.Currency = ccy
	}
	if resolved.Currency == "" {
		resolved.Currency = domain.DefaultCurrency
	}
	return resolved, nil
}

// --- event payloads (kept small + stable; consumers project these) ---

func billOpenedEvent(b domain.Bill, t domain.Totals) map[string]any {
	return map[string]any{
		"bill_id":     b.ID,
		"table":       b.Table,
		"order_ids":   b.OrderIDs,
		"total_minor": t.Total.Minor,
		"currency":    b.Currency,
	}
}

func discountEvent(b domain.Bill, minor int64, reason string) map[string]any {
	return map[string]any{
		"bill_id": b.ID,
		"table":   b.Table,
		"minor":   minor,
		"reason":  reason,
	}
}

func paymentEvent(b domain.Bill, p domain.Payment) map[string]any {
	return map[string]any{
		"bill_id":      b.ID,
		"payment_id":   p.ID,
		"method":       p.Method,
		"amount_minor": p.Amount.Minor,
		"currency":     b.Currency,
	}
}

func finalizedEvent(b domain.Bill, t domain.Totals) map[string]any {
	return map[string]any{
		"bill_id":     b.ID,
		"table":       b.Table,
		"order_ids":   b.OrderIDs,
		"total_minor": t.Total.Minor,
		"currency":    b.Currency,
	}
}
