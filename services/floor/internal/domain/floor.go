// Package domain holds the pure Floor model: the Floor aggregate (one per
// restaurant), its tables, seat/assign/move-or-swap rules, the DERIVED per-table
// status function, and the nudge engine. It imports NO infrastructure (no pgx,
// nats, connect) — only pkg/ids. Ported from the proven Restorna Node floor +
// orchestration (createFloor, seat/assign/moveOrSwap, buildFloorView, the nudge
// engine). Adapters map to/from proto + SQL; the app orchestrates.
package domain

import (
	"errors"
	"sort"
	"strings"
)

// FloorID is the single floor-doc id per tenant (one dining room per outlet).
const FloorID = "floor"

// Stored table statuses — the floor's OWN seating machine (free|seated). The live
// cooking/ready/billing values are DERIVED at read time and never stored, so a
// stored status can never go stale (a table runs many orders at once).
const (
	StatusFree    = "free"
	StatusSeated  = "seated"
	StatusCooking = "cooking" // derived only
	StatusReady   = "ready"   // derived only
	StatusBilling = "billing" // derived only
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// Table is one table on the floor. The stored doc holds the seat/order/waiter and
// the nudge timestamps; status is stored as the seating value (free|seated) but
// OVERRIDDEN by the derived value in GetFloor.
type Table struct {
	N            int32
	Status       string // stored seating status (free|seated); derived in GetFloor
	Order        string
	WaiterID     string
	SeatedAt     int64 // epoch ms; nudge timers (0 = unset)
	GreetedAt    int64
	LastServedAt int64
	LastCheckinAt int64
}

// Floor is the dining-room aggregate: an ordered set of tables for one tenant.
type Floor struct {
	ID     string
	Tables []Table
}

// NewFloor builds a floor from a list of table numbers. Duplicates are rejected
// (ErrInvalid). Every table starts free with all timers unset.
func NewFloor(tableNumbers []int32) (Floor, error) {
	seen := map[int32]struct{}{}
	tables := make([]Table, 0, len(tableNumbers))
	for _, n := range tableNumbers {
		if n <= 0 {
			return Floor{}, fieldErr("table numbers must be positive")
		}
		if _, dup := seen[n]; dup {
			return Floor{}, fieldErr("table listed more than once")
		}
		seen[n] = struct{}{}
		tables = append(tables, Table{N: n, Status: StatusFree})
	}
	sort.Slice(tables, func(i, j int) bool { return tables[i].N < tables[j].N })
	return Floor{ID: FloorID, Tables: tables}, nil
}

// Find returns a pointer to the table with number n, or nil.
func (f *Floor) Find(n int32) *Table {
	for i := range f.Tables {
		if f.Tables[i].N == n {
			return &f.Tables[i]
		}
	}
	return nil
}

// EnsureTable adds table n (free) if it is not already on the floor. Returns true
// if it was added. Used by the order-placed choreography so an order can seat its
// table even if the floor never listed it.
func (f *Floor) EnsureTable(n int32) bool {
	if n <= 0 {
		return false
	}
	if f.Find(n) != nil {
		return false
	}
	f.Tables = append(f.Tables, Table{N: n, Status: StatusFree})
	sort.Slice(f.Tables, func(i, j int) bool { return f.Tables[i].N < f.Tables[j].N })
	return true
}

// Seat marks a table seated and arms the greet timer (sets seatedAt to nowMs on
// FIRST seating only, and clears greetedAt so a fresh party is greeted). Order may
// be "" to leave the current order untouched. Returns ErrNotFound if absent.
func (f *Floor) Seat(n int32, order string, nowMs int64) error {
	t := f.Find(n)
	if t == nil {
		return ErrNotFound
	}
	t.Status = StatusSeated
	if order != "" {
		t.Order = order
	}
	// Arm the greet timer on first seating; don't reset it on later rounds.
	if t.SeatedAt == 0 {
		t.SeatedAt = nowMs
		t.GreetedAt = 0
	}
	return nil
}

// SetOrder records the live order on a table (used by the order-placed consumer).
func (f *Floor) SetOrder(n int32, order string) error {
	t := f.Find(n)
	if t == nil {
		return ErrNotFound
	}
	if order != "" {
		t.Order = order
	}
	return nil
}

// Assign sets the waiter on a single table. Returns ErrNotFound if absent.
func (f *Floor) Assign(n int32, waiterID string) error {
	t := f.Find(n)
	if t == nil {
		return ErrNotFound
	}
	t.WaiterID = waiterID
	return nil
}

// MarkServed records a serve on a table (sets lastServedAt; arms the check-in
// timer). No-op if the table is absent (a served event for an untracked table
// must not wedge the consumer).
func (f *Floor) MarkServed(n int32, nowMs int64) {
	t := f.Find(n)
	if t == nil {
		return
	}
	t.LastServedAt = nowMs
}

// AckGreet records that the guests were greeted (greetedAt = nowMs), suppressing
// the greet nudge. Returns ErrNotFound if absent.
func (f *Floor) AckGreet(n int32, nowMs int64) error {
	t := f.Find(n)
	if t == nil {
		return ErrNotFound
	}
	t.GreetedAt = nowMs
	return nil
}

// AckCheckin records a check-in (lastCheckinAt = nowMs), suppressing the check-in
// nudge until a newer serve. Returns ErrNotFound if absent.
func (f *Floor) AckCheckin(n int32, nowMs int64) error {
	t := f.Find(n)
	if t == nil {
		return ErrNotFound
	}
	t.LastCheckinAt = nowMs
	return nil
}

// MoveOrSwap relocates the seat/order/waiter from src to dst.
//   - dst free  -> MOVE: dst takes src's seat/order/waiter; src resets to free.
//   - dst busy  -> SWAP: exchange seat/order/waiter (nudge timers move with the seat).
//
// Returns the verb ("moved"|"swapped"). ErrNotFound if a table is missing;
// ErrInvalid if src==dst or src is free (nothing to move). Nudge timers travel
// with the seat so a moved party keeps its greet/check-in cadence.
func (f *Floor) MoveOrSwap(srcN, dstN int32) (string, error) {
	if srcN == dstN {
		return "", fieldErr("source and destination are the same table")
	}
	src := f.Find(srcN)
	dst := f.Find(dstN)
	if src == nil || dst == nil {
		return "", ErrNotFound
	}
	if src.Status == StatusFree && src.Order == "" {
		return "", fieldErr("source table is free — nothing to move")
	}

	if dst.Status == StatusFree && dst.Order == "" {
		// MOVE: dst inherits the whole seat (status, order, waiter, timers).
		dst.Status, dst.Order, dst.WaiterID = src.Status, src.Order, src.WaiterID
		dst.SeatedAt, dst.GreetedAt = src.SeatedAt, src.GreetedAt
		dst.LastServedAt, dst.LastCheckinAt = src.LastServedAt, src.LastCheckinAt
		reset(src)
		return "moved", nil
	}

	// SWAP: exchange the two seats wholesale (timers included).
	*src, *dst = swapKeepN(*src, *dst)
	return "swapped", nil
}

// reset clears a table back to free/empty (keeps its number).
func reset(t *Table) {
	n := t.N
	*t = Table{N: n, Status: StatusFree}
}

// swapKeepN swaps everything but the table numbers between a and b.
func swapKeepN(a, b Table) (Table, Table) {
	na, nb := a.N, b.N
	a, b = b, a
	a.N, b.N = na, nb
	return a, b
}

// --- derived status (port of orchestration buildFloorView) ---

// TableLoad is the per-table tally of live kitchen work, grouped from the kitchen
// board (cooking) + serve queue (ready) by table number.
type TableLoad struct {
	Cooking int
	Ready   int
}

// DeriveStatus computes a table's LIVE status from its stored seating plus its
// kitchen load and whether it has an open bill. Priority reflects what most needs
// a human's attention:
//
//	billing > ready > cooking > seated > free
//
// (settle now > deliver now > kitchen busy > occupied/idle > empty). The stored
// seating status (free|seated) is the floor of this ladder.
func DeriveStatus(t Table, load TableLoad, hasOpenBill bool) string {
	switch {
	case hasOpenBill:
		return StatusBilling
	case load.Ready > 0:
		return StatusReady
	case load.Cooking > 0:
		return StatusCooking
	case t.Status == StatusFree && t.Order == "":
		return StatusFree
	default:
		return StatusSeated
	}
}

// TableNumber extracts the integer from a table label ("T7"/"7"/"Table 7" -> 7),
// or 0 if there are no digits. Used to group kitchen tickets / bills (which carry
// string table labels) onto the numeric floor tables.
func TableNumber(label string) int32 {
	var b strings.Builder
	for _, r := range label {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	s := b.String()
	if s == "" {
		return 0
	}
	var n int32
	for _, r := range s {
		n = n*10 + int32(r-'0')
	}
	return n
}

// --- helpers ---

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
