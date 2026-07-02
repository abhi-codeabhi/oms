// Package domain holds the pure billing model: the Bill aggregate (lines,
// discount, payments), the tax/total math, and the course Section grouping. It
// imports NO infrastructure (no pgx, nats, connect) — only pkg/ids and pkg/money.
// Ported from the proven Restorna Node billing domain (bill.js): openBill,
// computeTotals (GST + service charge on post-discount subtotal), applyDiscount,
// recordPayment (paid once payments cover the total), and the openTableBill
// section grouping (Appetizers/Mains/Breads/Sides/Drinks/Desserts/Other).
package domain

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixBill     = "bill"
	PrefixBillLine = "bl"
	PrefixPayment  = "pay"
)

// DefaultCurrency is used when the settings service does not pin one.
const DefaultCurrency = "INR"

// CategoryOther is the catch-all course for lines whose catalog category is
// unknown. It sorts last on the printed bill.
const CategoryOther = "Other"

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// TaxConfig is the effective billing configuration resolved from SettingsService
// (billing.gst_pct, billing.service_charge_pct, billing.rounding,
// billing.currency). The app reads it before computing a bill's totals.
type TaxConfig struct {
	GSTPct           float64
	ServiceChargePct float64
	Rounding         Rounding
	Currency         string
}

// Rounding controls how the grand total is rounded to a whole currency unit.
// Mirrors the Node demo's billing.rounding setting ("nearest_1"/"none").
type Rounding string

const (
	RoundNone     Rounding = "none"       // no rounding (default)
	RoundNearest1 Rounding = "nearest_1"  // round total to the nearest whole unit
	RoundUp1      Rounding = "up_1"       // round total up to the next whole unit
	RoundDown1    Rounding = "down_1"     // round total down to the whole unit
)

// ParseRounding normalises a settings string to a Rounding (unknown -> none).
func ParseRounding(s string) Rounding {
	switch Rounding(strings.ToLower(strings.TrimSpace(s))) {
	case RoundNearest1:
		return RoundNearest1
	case RoundUp1:
		return RoundUp1
	case RoundDown1:
		return RoundDown1
	default:
		return RoundNone
	}
}

// BillLine is one priced line on the final bill (one per unit; qty is expanded
// when the bill is built so each unit groups into its course section).
type BillLine struct {
	ID       string
	Name     string
	Category string
	Price    money.Money
}

// Payment is one recorded tender against a bill (cash/card/upi/...).
type Payment struct {
	ID     string
	Method string
	Amount money.Money
	Ref    string
	At     time.Time
}

// Bill is the aggregated, settle-able dine-in bill for a table: every unbilled
// order's lines, a flat discount, payments, and a paid flag.
type Bill struct {
	ID           string
	RestaurantID string
	Table        string
	OrderIDs     []string
	Lines        []BillLine
	Discount     money.Money // accumulated flat discount (minor units)
	Payments     []Payment
	Paid         bool
	Currency     string
	CreatedAt    time.Time
}

// NewLineInput is a resolved bill line (name + category + per-unit price) used to
// build a bill. The app resolves name/category from catalog and expands qty into
// one input per unit before calling NewBill.
type NewLineInput struct {
	Name     string
	Category string
	Price    money.Money
}

// NewBill builds an open (unpaid) bill from resolved per-unit lines for a table.
// orderIDs are the contributing unbilled orders (so the app can MarkBilled them).
// Returns ErrInvalid if there are no lines or any line lacks a name. The bill's
// currency is taken from the first line (the app resolves it from settings).
func NewBill(restaurantID, table string, orderIDs []string, lines []NewLineInput, now time.Time) (Bill, error) {
	restaurantID = strings.TrimSpace(restaurantID)
	table = strings.TrimSpace(table)
	if restaurantID == "" {
		return Bill{}, fieldErr("restaurant_id is required")
	}
	if table == "" {
		return Bill{}, fieldErr("table is required")
	}
	if len(lines) == 0 {
		return Bill{}, fieldErr("at least one line is required")
	}

	currency := DefaultCurrency
	if lines[0].Price.Currency != "" {
		currency = lines[0].Price.Currency
	}

	billLines := make([]BillLine, 0, len(lines))
	for i, l := range lines {
		name := strings.TrimSpace(l.Name)
		if name == "" {
			return Bill{}, fieldErr("lines[" + itoa(i) + "].name is required")
		}
		cat := strings.TrimSpace(l.Category)
		if cat == "" {
			cat = CategoryOther
		}
		price := l.Price
		if price.Currency == "" {
			price.Currency = currency
		}
		billLines = append(billLines, BillLine{
			ID:       ids.New(PrefixBillLine),
			Name:     name,
			Category: cat,
			Price:    price,
		})
	}

	orders := append([]string(nil), orderIDs...)
	return Bill{
		ID:           ids.New(PrefixBill),
		RestaurantID: restaurantID,
		Table:        table,
		OrderIDs:     orders,
		Lines:        billLines,
		Discount:     money.New(0, currency),
		Payments:     nil,
		Paid:         false,
		Currency:     currency,
		CreatedAt:    now.UTC(),
	}, nil
}

// Totals is the computed money breakdown for a bill under a TaxConfig.
type Totals struct {
	Subtotal      money.Money // sum of line prices (pre-discount, pre-tax)
	Discount      money.Money // applied flat discount
	ServiceCharge money.Money // service_charge_pct of the post-discount subtotal
	Tax           money.Money // gst_pct of the post-discount subtotal
	Total         money.Money // taxable + service charge + tax (rounded)
}

// Subtotal sums every line price (pre-discount, pre-tax).
func (b Bill) Subtotal() money.Money {
	out := money.New(0, b.Currency)
	for _, l := range b.Lines {
		// currencies are normalised to the bill currency at build time.
		out = money.New(out.Minor+l.Price.Minor, b.Currency)
	}
	return out
}

// PaidMinor returns the sum of recorded payments (minor units).
func (b Bill) PaidMinor() int64 {
	var s int64
	for _, p := range b.Payments {
		s += p.Amount.Minor
	}
	return s
}

// ComputeTotals applies the tax config to the bill: GST and service charge are
// both computed on the POST-DISCOUNT subtotal (taxable base), matching the Node
// demo. The grand total is then rounded per cfg.Rounding. Discount is clamped so
// the taxable base never goes negative.
func (b Bill) ComputeTotals(cfg TaxConfig) Totals {
	ccy := b.Currency
	if ccy == "" {
		ccy = cfg.Currency
	}
	if ccy == "" {
		ccy = DefaultCurrency
	}
	subtotal := b.Subtotal()

	discMinor := b.Discount.Minor
	if discMinor < 0 {
		discMinor = 0
	}
	if discMinor > subtotal.Minor {
		discMinor = subtotal.Minor // never discount below zero
	}
	discount := money.New(discMinor, ccy)

	taxable := money.New(subtotal.Minor-discMinor, ccy)
	serviceCharge := taxable.Pct(cfg.ServiceChargePct)
	tax := taxable.Pct(cfg.GSTPct)

	totalMinor := taxable.Minor + serviceCharge.Minor + tax.Minor
	totalMinor = applyRounding(totalMinor, cfg.Rounding)
	total := money.New(totalMinor, ccy)

	return Totals{
		Subtotal:      money.New(subtotal.Minor, ccy),
		Discount:      discount,
		ServiceCharge: serviceCharge,
		Tax:           tax,
		Total:         total,
	}
}

// ApplyDiscount adds a flat discount (minor units) to the bill. Positive minor
// only; the new discount accumulates on top of any prior discount. Returns
// ErrInvalid for a non-positive amount.
func (b *Bill) ApplyDiscount(minor int64) error {
	if minor <= 0 {
		return fieldErr("discount must be a positive integer (minor units)")
	}
	b.Discount = money.New(b.Discount.Minor+minor, b.Currency)
	return nil
}

// RecordPayment appends a payment and recomputes the paid flag against the
// computed total under cfg. Returns the appended Payment. paid becomes true once
// the sum of payments covers the total. Returns ErrInvalid for an unknown method
// or non-positive amount, or if the bill is already fully paid.
func (b *Bill) RecordPayment(method string, amountMinor int64, ref string, cfg TaxConfig, now time.Time) (Payment, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	if !validMethod(method) {
		return Payment{}, fieldErr("unknown payment method: " + method)
	}
	if amountMinor <= 0 {
		return Payment{}, fieldErr("amount must be a positive integer (minor units)")
	}
	if b.Paid {
		return Payment{}, fieldErr("bill is already fully paid")
	}
	p := Payment{
		ID:     ids.New(PrefixPayment),
		Method: method,
		Amount: money.New(amountMinor, b.Currency),
		Ref:    strings.TrimSpace(ref),
		At:     now.UTC(),
	}
	b.Payments = append(b.Payments, p)

	total := b.ComputeTotals(cfg).Total
	if b.PaidMinor() >= total.Minor {
		b.Paid = true
	}
	return p, nil
}

// Section is one course grouping on the printed bill: a category with its line
// count and subtotal.
type Section struct {
	Category string
	Count    int32
	Subtotal money.Money
}

// categoryOrder is the conventional menu running order; unknown categories sort
// to the end (mirrors the Node demo's CATEGORY_ORDER).
var categoryOrder = []string{"Appetizers", "Mains", "Breads", "Sides", "Drinks", "Desserts", "Other"}

func categoryRank(cat string) int {
	for i, c := range categoryOrder {
		if c == cat {
			return i
		}
	}
	return len(categoryOrder) + 1
}

// Sections groups the bill's lines into priced course sections, ordered by the
// conventional menu running order (Appetizers → … → Desserts → Other).
func (b Bill) Sections() []Section {
	type agg struct {
		count    int32
		subtotal int64
	}
	byCat := map[string]*agg{}
	order := []string{}
	for _, l := range b.Lines {
		cat := l.Category
		if cat == "" {
			cat = CategoryOther
		}
		a, ok := byCat[cat]
		if !ok {
			a = &agg{}
			byCat[cat] = a
			order = append(order, cat)
		}
		a.count++
		a.subtotal += l.Price.Minor
	}
	out := make([]Section, 0, len(order))
	for _, cat := range order {
		a := byCat[cat]
		out = append(out, Section{
			Category: cat,
			Count:    a.count,
			Subtotal: money.New(a.subtotal, b.Currency),
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := categoryRank(out[i].Category), categoryRank(out[j].Category)
		if ri == rj {
			return out[i].Category < out[j].Category
		}
		return ri < rj
	})
	return out
}

// --- helpers ---

func validMethod(m string) bool {
	switch m {
	case "card", "cash", "upi", "split", "wallet", "netbanking":
		return true
	default:
		return false
	}
}

// applyRounding rounds a minor-unit total to a whole currency unit per mode.
// 100 minor units = 1 major unit (paise -> rupee).
func applyRounding(minor int64, mode Rounding) int64 {
	const unit = 100
	switch mode {
	case RoundNearest1:
		rem := minor % unit
		if rem == 0 {
			return minor
		}
		if rem >= unit/2 {
			return minor - rem + unit
		}
		return minor - rem
	case RoundUp1:
		rem := minor % unit
		if rem == 0 {
			return minor
		}
		return minor - rem + unit
	case RoundDown1:
		return minor - (minor % unit)
	default:
		return minor
	}
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }

// itoa avoids importing strconv just for index messages.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
