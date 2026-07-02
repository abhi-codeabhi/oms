// Package nats holds the event consumer that drives kitchen choreography: it
// subscribes to restorna.ordering.order.placed.v1 and, for each order, calls the
// app's OnOrderPlaced (which resolves names/stations from catalog and fires a
// ticket). Delivery is idempotent: pkg/eventbus/nats dedupes on Event.ID in
// process and the app marks the event id processed in the same tx as the insert,
// so a redelivery (even across restarts) is a no-op.
package nats

import (
	"context"
	"encoding/json"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/events"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/kitchen/internal/app"
)

// SubjectOrderPlaced is the event type/subject the kitchen subscribes to.
const SubjectOrderPlaced = "restorna.ordering.order.placed.v1"

// durable is the JetStream durable consumer name (one logical kitchen consumer).
const durable = "kitchen-order-placed"

// orderPlacedData is the JSON shape of the ordering.order.placed.v1 payload:
// { order_id, restaurant_id, table_id, lines[] }. Lines carry the resolved menu
// item id + (optionally) a name/station the ordering service already knew.
type orderPlacedData struct {
	OrderID      string `json:"order_id"`
	RestaurantID string `json:"restaurant_id"`
	TableID      string `json:"table_id"`
	Lines        []struct {
		ID         string `json:"id"`
		MenuItemID string `json:"menu_item_id"`
		Name       string `json:"name"`
		Station    string `json:"station"`
		Qty        int    `json:"qty"`
	} `json:"lines"`
}

// Consumer wires the order-placed subscription to the app use case.
type Consumer struct {
	uc      *app.App
	natsURL string
}

// New builds a Consumer for the given app and NATS url.
func New(uc *app.App, natsURL string) *Consumer {
	return &Consumer{uc: uc, natsURL: natsURL}
}

// Run subscribes and blocks until ctx is done. Intended to run as a goroutine
// from main. Each event is decoded, scoped to its restaurant, and handed to
// OnOrderPlaced; a handler error nak's the message for redelivery.
func (c *Consumer) Run(ctx context.Context) error {
	return eventbus.Subscribe(ctx, c.natsURL, SubjectOrderPlaced, durable, func(e events.Event) error {
		return c.handle(ctx, e)
	})
}

// handle decodes one order-placed event and fires a ticket. Exported logic kept
// here (not in app) so the app stays transport-free; this is pure mapping +
// tenancy wiring.
func (c *Consumer) handle(ctx context.Context, e events.Event) error {
	var d orderPlacedData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		// Poison payload: returning nil acks/drops it (eventbus already logged the
		// raw bytes upstream); a malformed event must not wedge the consumer.
		return nil
	}

	restaurantID := d.RestaurantID
	if restaurantID == "" {
		// Fall back to the envelope tenant if the payload omitted it.
		restaurantID = e.TenantID
	}

	// Scope the outgoing catalog call + the repo tx to this restaurant. The
	// catalog client reads tenancy from ctx; the kitchen never trusts a body id
	// for tenancy, only this scope derived from the trusted event envelope.
	ctx = tenancy.With(ctx, tenancy.Scope{
		RestaurantID: restaurantID,
		Role:         commonv1.Role_ROLE_KITCHEN,
	})

	lines := make([]app.OrderPlacedLine, 0, len(d.Lines))
	for _, ln := range d.Lines {
		lines = append(lines, app.OrderPlacedLine{
			MenuItemID: ln.MenuItemID,
			Name:       ln.Name,
			Station:    ln.Station,
			Qty:        ln.Qty,
		})
	}

	if err := c.uc.OnOrderPlaced(ctx, app.OrderPlaced{
		EventID:      e.ID,
		OrderID:      d.OrderID,
		RestaurantID: restaurantID,
		Table:        d.TableID,
		Lines:        lines,
	}); err != nil {
		return fmt.Errorf("kitchen consumer: order %s: %w", d.OrderID, err)
	}
	return nil
}
