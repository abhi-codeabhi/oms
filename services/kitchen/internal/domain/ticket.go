// Package domain holds the pure Kitchen (KDS) model: the Ticket aggregate and its
// per-item state machine. It imports NO infrastructure (no pgx, nats, connect) —
// only pkg/ids for ULID generation. Rules live here; adapters map to/from proto
// and SQL. Ported from the proven Restorna Node KDS (new/prep/ready, bump, served,
// ticketPhase cooking/ready/served, getBoard=cooking, readyQueue=ready, allDay).
package domain

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixTicket = "tkt"
	PrefixItem   = "ti"
)

// ItemState is the per-item cook state. The integer value IS what is persisted
// and what the proto carries (TicketItem.state: 0 new, 1 prep, 2 ready).
type ItemState int32

const (
	StateNew   ItemState = 0 // fired, not started
	StatePrep  ItemState = 1 // being prepared
	StateReady ItemState = 2 // done at the pass
)

// stateReady is the terminal state value; advancing is capped here.
const stateReady = StateReady

// Phase is the lifecycle of a whole ticket, used to route it to the right surface:
//
//	PhaseCooking — still being made (shows on the kitchen board / GetBoard)
//	PhaseReady   — all items ready, not yet delivered (shows on the waiter ServeQueue)
//	PhaseServed  — delivered (off both)
type Phase string

const (
	PhaseCooking Phase = "cooking"
	PhaseReady   Phase = "ready"
	PhaseServed  Phase = "served"
)

// Stations the kitchen routes to. Unknown stations fall back to grill (mirrors the
// Node demo's tolerant routing so a bad catalog hint never drops a ticket).
const defaultStation = "grill"

var knownStations = map[string]struct{}{
	"grill":   {},
	"tandoor": {},
	"cold":    {},
}

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// TicketItem is one line on a fired ticket, routed to a station and progressing
// new -> prep -> ready.
type TicketItem struct {
	ID      string
	Name    string
	Station string
	State   ItemState
}

// Ticket is a fired order on the kitchen display.
type Ticket struct {
	ID        string
	OrderID   string
	Table     string
	Items     []TicketItem
	Served    bool // true once the waiter delivers THIS ticket to the table
	CreatedAt time.Time
}

// NewItemInput is a station-resolved line used to build a ticket (the catalog
// client supplies Name + Station before the ticket is created).
type NewItemInput struct {
	Name    string
	Station string
}

// NewTicket builds a ticket from an order. Each item starts at StateNew. Item
// names are required; station is normalised (unknown -> grill). Returns
// ErrInvalid if there are no items or any item lacks a name.
func NewTicket(orderID, table string, items []NewItemInput, now time.Time) (Ticket, error) {
	orderID = strings.TrimSpace(orderID)
	table = strings.TrimSpace(table)
	if orderID == "" {
		return Ticket{}, fieldErr("order_id is required")
	}
	if table == "" {
		return Ticket{}, fieldErr("table is required")
	}
	if len(items) == 0 {
		return Ticket{}, fieldErr("at least one item is required")
	}
	tItems := make([]TicketItem, 0, len(items))
	for i, it := range items {
		name := strings.TrimSpace(it.Name)
		if name == "" {
			return Ticket{}, fieldErr("items[" + itoa(i) + "].name is required")
		}
		tItems = append(tItems, TicketItem{
			ID:      ids.New(PrefixItem),
			Name:    name,
			Station: normaliseStation(it.Station),
			State:   StateNew,
		})
	}
	return Ticket{
		ID:        ids.New(PrefixTicket),
		OrderID:   orderID,
		Table:     table,
		Items:     tItems,
		Served:    false,
		CreatedAt: now.UTC(),
	}, nil
}

// AdvanceItem cycles a single item new -> prep -> ready (capped at ready) in place.
// Returns ErrInvalid if itemIndex is out of range.
func (t *Ticket) AdvanceItem(itemIndex int) error {
	if itemIndex < 0 || itemIndex >= len(t.Items) {
		return fieldErr("item_index out of range")
	}
	if t.Items[itemIndex].State < stateReady {
		t.Items[itemIndex].State++
	}
	return nil
}

// BumpAll jumps every item on the ticket to ready (the cook bumps the whole rail).
func (t *Ticket) BumpAll() {
	for i := range t.Items {
		t.Items[i].State = stateReady
	}
}

// MarkServed flags the ticket delivered. Idempotent.
func (t *Ticket) MarkServed() { t.Served = true }

// IsAllReady reports whether every item has reached ready (and there is at least
// one item).
func (t *Ticket) IsAllReady() bool {
	if len(t.Items) == 0 {
		return false
	}
	for _, it := range t.Items {
		if it.State != stateReady {
			return false
		}
	}
	return true
}

// Phase derives the routing phase: served beats ready beats cooking.
func (t *Ticket) Phase() Phase {
	switch {
	case t.Served:
		return PhaseServed
	case t.IsAllReady():
		return PhaseReady
	default:
		return PhaseCooking
	}
}

// CookingBoard returns the active KITCHEN board: tickets still cooking, oldest
// first (FIFO expo discipline). Ready (bumped) and served tickets have left the
// cook's screen.
func CookingBoard(tickets []Ticket) []Ticket {
	return filterByPhase(tickets, PhaseCooking)
}

// ServeQueue returns the WAITER serve queue: tickets all-ready but not yet
// delivered, oldest first. One entry per order so each round is served
// independently.
func ServeQueue(tickets []Ticket) []Ticket {
	return filterByPhase(tickets, PhaseReady)
}

// AllDayCounts is the "all-day rail": across all live tickets, how many of each
// item are NOT yet ready — the running work the kitchen still owes.
func AllDayCounts(tickets []Ticket) map[string]int32 {
	counts := make(map[string]int32)
	for _, t := range tickets {
		if t.Served {
			continue
		}
		for _, it := range t.Items {
			if it.State < stateReady {
				counts[it.Name]++
			}
		}
	}
	return counts
}

// --- helpers ---

func filterByPhase(tickets []Ticket, phase Phase) []Ticket {
	out := make([]Ticket, 0, len(tickets))
	for _, t := range tickets {
		if t.Phase() == phase {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

func normaliseStation(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if _, ok := knownStations[s]; ok {
		return s
	}
	return defaultStation
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
