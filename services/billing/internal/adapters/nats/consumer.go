// Package nats holds the event consumers that maintain the billing-board read
// model (the `tabs` projection). Billing is event-driven on the board side: it
// subscribes to
//   - restorna.ordering.order.placed.v1        -> add running total + counts
//   - restorna.servicerequests.raised.v1 (bill) -> mark the tab asked
//   - restorna.billing.bill.opened.v1           -> attach bill, flip to bill_ready
//   - restorna.billing.bill.finalized.v1        -> remove the tab
// Delivery is idempotent: pkg/eventbus/nats dedupes on Event.ID in process and the
// app marks the event id processed in the same tx as the projection write, so a
// redelivery (even across restarts) is a no-op.
package nats

import (
	"context"
	"encoding/json"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/events"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/billing/internal/app"
)

// Subjects (event types) the billing board consumes.
const (
	SubjectOrderPlaced  = "restorna.ordering.order.placed.v1"
	SubjectBillAsked    = "restorna.servicerequests.raised.v1"
	SubjectBillOpened   = "restorna.billing.bill.opened.v1"
	SubjectBillFinalized = "restorna.billing.bill.finalized.v1"
)

// Durable consumer names (one logical billing-board consumer per subject).
const (
	durOrderPlaced  = "billing-order-placed"
	durBillAsked    = "billing-bill-asked"
	durBillOpened   = "billing-bill-opened"
	durBillFinalized = "billing-bill-finalized"
)

// Consumer wires the board subscriptions to the app projection handlers.
type Consumer struct {
	uc      *app.App
	natsURL string
}

// New builds a Consumer for the given app and NATS url.
func New(uc *app.App, natsURL string) *Consumer {
	return &Consumer{uc: uc, natsURL: natsURL}
}

// Run starts every board subscription and blocks until ctx is done. Each
// subscription runs in its own goroutine; the first to error returns it.
func (c *Consumer) Run(ctx context.Context) error {
	errs := make(chan error, 4)
	go func() {
		errs <- eventbus.Subscribe(ctx, c.natsURL, SubjectOrderPlaced, durOrderPlaced, c.handleOrderPlaced(ctx))
	}()
	go func() {
		errs <- eventbus.Subscribe(ctx, c.natsURL, SubjectBillAsked, durBillAsked, c.handleBillAsked(ctx))
	}()
	go func() {
		errs <- eventbus.Subscribe(ctx, c.natsURL, SubjectBillOpened, durBillOpened, c.handleBillOpened(ctx))
	}()
	go func() {
		errs <- eventbus.Subscribe(ctx, c.natsURL, SubjectBillFinalized, durBillFinalized, c.handleBillFinalized(ctx))
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errs:
		return err
	}
}

// scopeCtx derives the tenancy scope from the trusted event envelope/payload so
// the projection tx is RLS-scoped to the restaurant (never a request-body id).
func scopeCtx(ctx context.Context, restaurantID string) context.Context {
	return tenancy.With(ctx, tenancy.Scope{RestaurantID: restaurantID, Role: commonv1.Role_ROLE_CASHIER})
}

// --- order.placed -> AddOrder ---

type orderPlacedData struct {
	OrderID      string `json:"order_id"`
	RestaurantID string `json:"restaurant_id"`
	TableID      string `json:"table_id"`
	Lines        []struct {
		Qty       int32 `json:"qty"`
		UnitPrice struct {
			Minor    int64  `json:"minor"`
			Currency string `json:"currency"`
		} `json:"unit_price"`
	} `json:"lines"`
}

func (c *Consumer) handleOrderPlaced(ctx context.Context) func(events.Event) error {
	return func(e events.Event) error {
		var d orderPlacedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil // poison payload: drop, never wedge the consumer
		}
		restaurantID := d.RestaurantID
		if restaurantID == "" {
			restaurantID = e.TenantID
		}
		var units int32
		var subtotal int64
		currency := ""
		for _, ln := range d.Lines {
			qty := ln.Qty
			if qty < 1 {
				qty = 1
			}
			units += qty
			subtotal += ln.UnitPrice.Minor * int64(qty)
			if currency == "" {
				currency = ln.UnitPrice.Currency
			}
		}
		if err := c.uc.OnOrderPlaced(scopeCtx(ctx, restaurantID), app.OrderPlaced{
			EventID:       e.ID,
			RestaurantID:  restaurantID,
			OrderID:       d.OrderID,
			Table:         d.TableID,
			ItemUnits:     units,
			SubtotalMinor: subtotal,
			Currency:      currency,
		}); err != nil {
			return fmt.Errorf("billing board: order %s: %w", d.OrderID, err)
		}
		return nil
	}
}

// --- servicerequests.raised (type=bill) -> MarkAsked ---

type raisedData struct {
	Type         string `json:"type"`
	Table        int32  `json:"table"`
	TableID      string `json:"table_id"`
	RestaurantID string `json:"restaurant_id"`
}

func (c *Consumer) handleBillAsked(ctx context.Context) func(events.Event) error {
	return func(e events.Event) error {
		var d raisedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil
		}
		if d.Type != "bill" {
			return nil // only the "bill" request type flips the board
		}
		restaurantID := d.RestaurantID
		if restaurantID == "" {
			restaurantID = e.TenantID
		}
		table := d.TableID
		if table == "" && d.Table != 0 {
			table = fmt.Sprintf("%d", d.Table)
		}
		if err := c.uc.OnBillAsked(scopeCtx(ctx, restaurantID), app.BillAsked{
			EventID:      e.ID,
			RestaurantID: restaurantID,
			Table:        table,
		}); err != nil {
			return fmt.Errorf("billing board: bill asked table %s: %w", table, err)
		}
		return nil
	}
}

// --- bill.opened -> AttachBill (bill_ready) ---

type billOpenedData struct {
	BillID     string `json:"bill_id"`
	Table      string `json:"table"`
	TotalMinor int64  `json:"total_minor"`
	Currency   string `json:"currency"`
}

func (c *Consumer) handleBillOpened(ctx context.Context) func(events.Event) error {
	return func(e events.Event) error {
		var d billOpenedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil
		}
		if err := c.uc.OnBillOpened(scopeCtx(ctx, e.TenantID), app.BillOpened{
			EventID:      e.ID,
			RestaurantID: e.TenantID,
			BillID:       d.BillID,
			Table:        d.Table,
			TotalMinor:   d.TotalMinor,
			Currency:     d.Currency,
		}); err != nil {
			return fmt.Errorf("billing board: bill opened %s: %w", d.BillID, err)
		}
		return nil
	}
}

// --- bill.finalized -> remove tab ---

type billFinalizedData struct {
	BillID string `json:"bill_id"`
	Table  string `json:"table"`
}

func (c *Consumer) handleBillFinalized(ctx context.Context) func(events.Event) error {
	return func(e events.Event) error {
		var d billFinalizedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil
		}
		if err := c.uc.OnBillFinalized(scopeCtx(ctx, e.TenantID), app.BillFinalized{
			EventID:      e.ID,
			RestaurantID: e.TenantID,
			BillID:       d.BillID,
			Table:        d.Table,
		}); err != nil {
			return fmt.Errorf("billing board: bill finalized %s: %w", d.BillID, err)
		}
		return nil
	}
}
