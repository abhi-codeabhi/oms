// Package domain holds the pure ordering model: an Order is a multi-round dine-in
// record for a table, made of priced Lines, with a computed subtotal and a billed
// flag. It imports NO infrastructure (no pgx, nats, connect). Rules live here;
// adapters map this to/from proto and SQL.
//
// Ported from the proven Node ordering service (order/line model, subtotal,
// tolerant table matching). Money is integer minor units (CONVENTIONS.md).
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixOrder = "ord"
	PrefixLine  = "ln"
)

// defaultCurrency is the platform default minor-unit currency (INR/paise).
const defaultCurrency = "INR"

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// Line is a single priced item on an order. unit_price is integer minor units.
type Line struct {
	ID         string
	MenuItemID string
	Name       string // resolved dish name
	Qty        int32
	UnitPrice  money.Money
	Station    string
}

// LineTotal is unit_price * qty.
func (l Line) LineTotal() money.Money {
	return money.New(l.UnitPrice.Minor*int64(l.Qty), l.UnitPrice.Currency)
}

// NewLineInput is the validated input describing one line to build.
type NewLineInput struct {
	MenuItemID string
	Name       string
	Qty        int32
	UnitPrice  money.Money
	Station    string
}

// Order is a multi-round dine-in order for a table. No payment lives here — the
// bill is settled later by billing. PlaceOrder emits OrderPlaced for kitchen+floor.
type Order struct {
	ID           string
	RestaurantID string
	TableID      string // "T7"
	Lines        []Line
	Subtotal     money.Money
	Billed       bool
	CreatedAt    time.Time
}

// NewOrder builds and validates an order from raw lines, computing the subtotal
// as the sum of unit_price * qty across lines (the proven Node rule). A line id is
// minted per line; the order id is minted here (in the domain, not the DB).
func NewOrder(restaurantID, tableID string, items []NewLineInput, now time.Time) (Order, error) {
	if strings.TrimSpace(restaurantID) == "" {
		return Order{}, fieldErr("restaurant_id is required")
	}
	if tableID = strings.TrimSpace(tableID); tableID == "" {
		return Order{}, fieldErr("table_id is required")
	}
	if len(items) == 0 {
		return Order{}, fieldErr("at least one line is required")
	}

	lines := make([]Line, 0, len(items))
	subtotal := money.New(0, defaultCurrency)
	for i, it := range items {
		if strings.TrimSpace(it.MenuItemID) == "" {
			return Order{}, fieldErr("line: menu_item_id is required")
		}
		if it.Qty <= 0 {
			return Order{}, fieldErr("line: qty must be positive")
		}
		ccy := it.UnitPrice.Currency
		if ccy == "" {
			ccy = defaultCurrency
		}
		// Keep the order single-currency: the subtotal accumulator drives it.
		if i == 0 {
			subtotal = money.New(0, ccy)
		}
		name := strings.TrimSpace(it.Name)
		if name == "" {
			name = it.MenuItemID
		}
		line := Line{
			ID:         ids.New(PrefixLine),
			MenuItemID: it.MenuItemID,
			Name:       name,
			Qty:        it.Qty,
			UnitPrice:  money.New(it.UnitPrice.Minor, ccy),
			Station:    strings.TrimSpace(it.Station),
		}
		sum, err := subtotal.Add(line.LineTotal())
		if err != nil {
			return Order{}, fieldErr("all lines must share one currency")
		}
		subtotal = sum
		lines = append(lines, line)
	}

	return Order{
		ID:           ids.New(PrefixOrder),
		RestaurantID: restaurantID,
		TableID:      tableID,
		Lines:        lines,
		Subtotal:     subtotal,
		Billed:       false,
		CreatedAt:    now.UTC(),
	}, nil
}

// MarkBilled flips the billed flag so a finalized bill never includes the order
// twice (the proven Node markBilled rule).
func (o *Order) MarkBilled() { o.Billed = true }

// Relocate moves the order to a different table label (waiter move/swap).
func (o *Order) Relocate(toTable string) error {
	if toTable = strings.TrimSpace(toTable); toTable == "" {
		return fieldErr("to_table is required")
	}
	o.TableID = toTable
	return nil
}

// TableKey normalises a table label for tolerant matching: "T7", "7", 7 all map to
// "7". Non-digits are stripped; if nothing remains the trimmed original is used so
// non-numeric labels (e.g. "PATIO") still match themselves. Ported verbatim from
// the Node service's key() helper.
func TableKey(v string) string {
	var b strings.Builder
	for _, r := range v {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if d := b.String(); d != "" {
		return d
	}
	return strings.TrimSpace(v)
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
