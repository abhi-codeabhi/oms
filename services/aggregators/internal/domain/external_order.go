// Package domain holds the pure aggregators model: the ExternalOrder aggregate
// (a delivery order ingested from Zomato/Swiggy) and its status lifecycle. It
// imports NO infrastructure (no pgx, nats, connect) — only pkg/ids for ULID
// generation and pkg/money for line prices. Rules live here; adapters map to/from
// proto and SQL.
package domain

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// PrefixExternalOrder is the type-prefixed ULID for an ExternalOrder (CONVENTIONS.md).
const PrefixExternalOrder = "ext"

// Domain errors. The grpc adapter maps these to Connect codes.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// Status is the lifecycle of an external (aggregator) order. Received is the
// entry state (webhook arrived); accepted/preparing/ready/dispatched track the
// restaurant's progress pushed back upstream; cancelled/rejected are terminal.
type Status string

const (
	StatusReceived   Status = "received"
	StatusAccepted   Status = "accepted"
	StatusPreparing  Status = "preparing"
	StatusReady      Status = "ready"
	StatusDispatched Status = "dispatched"
	StatusCancelled  Status = "cancelled"
	StatusRejected   Status = "rejected"
)

// validStatuses is the set of statuses an Ack may set.
var validStatuses = map[Status]struct{}{
	StatusReceived:   {},
	StatusAccepted:   {},
	StatusPreparing:  {},
	StatusReady:      {},
	StatusDispatched: {},
	StatusCancelled:  {},
	StatusRejected:   {},
}

// Item is one line on an external order.
type Item struct {
	Name  string
	Qty   int32
	Price money.Money
}

// ExternalOrder is a delivery-aggregator order ingested via connector-hub and
// forwarded into ordering. It is scoped to one restaurant (the tenant key) and
// uniquely identified upstream by (ConnectorID, ExternalRef).
type ExternalOrder struct {
	ID           string
	RestaurantID string
	ConnectorID  string // zomato|swiggy|mockagg
	ExternalRef  string // the aggregator's order id
	Status       Status
	Items        []Item
	PlacedAt     string // aggregator's placed_at (as reported), free-form RFC3339
	CreatedAt    time.Time
}

// NewExternalOrderInput is the validated input to create an ExternalOrder from a
// normalized aggregator-order event.
type NewExternalOrderInput struct {
	RestaurantID string
	ConnectorID  string
	ExternalRef  string
	Status       string
	Items        []Item
	PlacedAt     string
}

// NewExternalOrder builds an ExternalOrder from an ingested webhook event.
// RestaurantID, ConnectorID and ExternalRef are required. Status defaults to
// received when blank; an unknown status is rejected.
func NewExternalOrder(in NewExternalOrderInput, now time.Time) (ExternalOrder, error) {
	rid := strings.TrimSpace(in.RestaurantID)
	cid := strings.TrimSpace(in.ConnectorID)
	ref := strings.TrimSpace(in.ExternalRef)
	if rid == "" {
		return ExternalOrder{}, fieldErr("restaurant_id is required")
	}
	if cid == "" {
		return ExternalOrder{}, fieldErr("connector_id is required")
	}
	if ref == "" {
		return ExternalOrder{}, fieldErr("external_ref is required")
	}
	if len(in.Items) == 0 {
		return ExternalOrder{}, fieldErr("at least one item is required")
	}
	status := Status(strings.TrimSpace(in.Status))
	if status == "" {
		status = StatusReceived
	}
	if _, ok := validStatuses[status]; !ok {
		return ExternalOrder{}, fieldErr("unknown status: " + string(status))
	}
	return ExternalOrder{
		ID:           ids.New(PrefixExternalOrder),
		RestaurantID: rid,
		ConnectorID:  cid,
		ExternalRef:  ref,
		Status:       status,
		Items:        in.Items,
		PlacedAt:     in.PlacedAt,
		CreatedAt:    now.UTC(),
	}, nil
}

// SetStatus transitions the order to a new status (accept/reject/update from the
// AckExternalOrder RPC). An empty or unknown status is rejected.
func (o *ExternalOrder) SetStatus(s string) error {
	next := Status(strings.TrimSpace(s))
	if next == "" {
		return fieldErr("status is required")
	}
	if _, ok := validStatuses[next]; !ok {
		return fieldErr("unknown status: " + string(next))
	}
	o.Status = next
	return nil
}

// SyntheticTable returns the dine-in table label used when this order is
// forwarded to ordering, e.g. "AGG-ZOM9001". Ordering/kitchen/floor then treat
// it like any table so the aggregator order flows to the kitchen unchanged.
func (o *ExternalOrder) SyntheticTable() string {
	return "AGG-" + o.ExternalRef
}

// SortByCreatedAt orders a slice oldest-first (stable on id for equal times) —
// the list RPC returns a deterministic order.
func SortByCreatedAt(orders []ExternalOrder) {
	sort.SliceStable(orders, func(i, j int) bool {
		if orders[i].CreatedAt.Equal(orders[j].CreatedAt) {
			return orders[i].ID < orders[j].ID
		}
		return orders[i].CreatedAt.Before(orders[j].CreatedAt)
	})
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
