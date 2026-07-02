// Package nats holds the floor's event consumers, which drive its choreography:
//
//   - restorna.ordering.order.placed.v1  -> ensure+seat the table (arm greet timer,
//     record the order id)
//   - restorna.kitchen.ticket.served.v1  -> record the serve (arm the check-in timer)
//
// Both are idempotent: pkg/eventbus/nats dedupes on Event.ID in process, and the
// app marks the event id processed in the same tx as the floor write, so a
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
	"github.com/restorna/platform/services/floor/internal/app"
)

// Subjects the floor subscribes to and the durable consumer names (one logical
// floor consumer per subject).
const (
	SubjectOrderPlaced  = "restorna.ordering.order.placed.v1"
	SubjectTicketServed = "restorna.kitchen.ticket.served.v1"

	durableOrderPlaced  = "floor-order-placed"
	durableTicketServed = "floor-ticket-served"
)

// Consumer wires both subscriptions to the floor app use cases.
type Consumer struct {
	uc      *app.App
	natsURL string
}

// New builds a Consumer for the given app and NATS url.
func New(uc *app.App, natsURL string) *Consumer {
	return &Consumer{uc: uc, natsURL: natsURL}
}

// orderPlacedData is the JSON shape of ordering.order.placed.v1:
// { order_id, restaurant_id, table_id, lines[] }.
type orderPlacedData struct {
	OrderID      string `json:"order_id"`
	RestaurantID string `json:"restaurant_id"`
	TableID      string `json:"table_id"`
}

// ticketServedData is the JSON shape of kitchen.ticket.served.v1:
// { ticket_id, order_id, table }.
type ticketServedData struct {
	TicketID string `json:"ticket_id"`
	OrderID  string `json:"order_id"`
	Table    string `json:"table"`
}

// RunOrderPlaced subscribes to ordering.order.placed and blocks until ctx is done.
// Intended to run as a goroutine from main. On each event it ensures+seats the
// table (setting seated_at if unset) and records the order.
func (c *Consumer) RunOrderPlaced(ctx context.Context) error {
	return eventbus.Subscribe(ctx, c.natsURL, SubjectOrderPlaced, durableOrderPlaced, func(e events.Event) error {
		var d orderPlacedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil // poison payload: drop, don't wedge the consumer
		}
		restaurantID := d.RestaurantID
		if restaurantID == "" {
			restaurantID = e.TenantID
		}
		ctx := tenancy.With(ctx, tenancy.Scope{
			RestaurantID: restaurantID,
			Role:         commonv1.Role_ROLE_WAITER,
		})
		if err := c.uc.OnOrderPlaced(ctx, restaurantID, e.ID, d.TableID, d.OrderID); err != nil {
			return fmt.Errorf("floor order-placed: order %s: %w", d.OrderID, err)
		}
		return nil
	})
}

// RunTicketServed subscribes to kitchen.ticket.served and blocks until ctx is
// done. On each event it records the serve on the table (arming the check-in nudge).
func (c *Consumer) RunTicketServed(ctx context.Context) error {
	return eventbus.Subscribe(ctx, c.natsURL, SubjectTicketServed, durableTicketServed, func(e events.Event) error {
		var d ticketServedData
		if err := json.Unmarshal(e.Data, &d); err != nil {
			return nil
		}
		restaurantID := e.TenantID
		ctx := tenancy.With(ctx, tenancy.Scope{
			RestaurantID: restaurantID,
			Role:         commonv1.Role_ROLE_WAITER,
		})
		if err := c.uc.OnTicketServed(ctx, restaurantID, e.ID, d.Table); err != nil {
			return fmt.Errorf("floor ticket-served: ticket %s: %w", d.TicketID, err)
		}
		return nil
	})
}
