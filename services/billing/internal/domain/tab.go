// Tab is the billing-board read model: one row per occupied table, maintained
// EVENT-DRIVEN (not derived from a query). It is updated by consumers:
//   - ordering.order.placed.v1     -> add running total + order/item counts
//   - servicerequests.raised.v1(bill) -> mark asked
//   - billing.bill.opened.v1        -> attach bill id/total, flip to bill_ready
//   - billing.bill.finalized.v1     -> remove the tab
//
// The status precedence is bill_ready (an open bill) > asked > open. The board is
// returned sorted by table number (mirrors the Node openTabs read-model).
package domain

import (
	"sort"
	"strconv"
	"strings"

	"github.com/restorna/platform/pkg/money"
)

// Tab statuses (the proto Tab.status string).
const (
	StatusOpen      = "open"
	StatusAsked     = "asked"
	StatusBillReady = "bill_ready"
)

// Tab is one table's live billing-board entry.
type Tab struct {
	RestaurantID string
	Table        int32       // numeric table label (T7 -> 7)
	OrderCount   int32       // unbilled orders seen for this table
	ItemCount    int32       // total item units across those orders
	Running      money.Money // running pre-tax total of unbilled orders
	Asked        bool        // a "bill" service request was raised
	BillID       string      // set once a bill is opened for the table
	BillTotal    money.Money // the opened bill's grand total
}

// Status derives the board status from the tab's flags: an open bill wins, then
// an explicit ask, else just open.
func (t Tab) Status() string {
	switch {
	case t.BillID != "":
		return StatusBillReady
	case t.Asked:
		return StatusAsked
	default:
		return StatusOpen
	}
}

// AddOrder folds a newly placed order into the tab's running total + counts. It
// is additive and idempotency is the consumer's responsibility (dedupe on event
// id). itemUnits is the sum of line quantities; addMinor is the order subtotal.
func (t *Tab) AddOrder(itemUnits int32, addMinor int64, currency string) {
	if t.Running.Currency == "" {
		t.Running = money.New(0, currency)
	}
	t.OrderCount++
	t.ItemCount += itemUnits
	t.Running = money.New(t.Running.Minor+addMinor, t.Running.Currency)
}

// MarkAsked flips the tab to "asked" (a bill service request was raised).
func (t *Tab) MarkAsked() { t.Asked = true }

// AttachBill records the opened bill on the tab, flipping it to bill_ready.
func (t *Tab) AttachBill(billID string, total money.Money) {
	t.BillID = billID
	t.BillTotal = total
}

// SortTabs orders tabs by table number ascending (stable for equal numbers).
func SortTabs(tabs []Tab) {
	sort.SliceStable(tabs, func(i, j int) bool { return tabs[i].Table < tabs[j].Table })
}

// TableNumber extracts the numeric table label from a tolerant string ("T7"/"7"/
// "table 7" -> 7). Returns 0 when no digits are present.
func TableNumber(label string) int32 {
	var b strings.Builder
	for _, r := range label {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return 0
	}
	n, err := strconv.Atoi(b.String())
	if err != nil {
		return 0
	}
	return int32(n)
}
