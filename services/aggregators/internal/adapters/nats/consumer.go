// Package nats holds the event consumer that drives aggregators choreography: it
// subscribes to restorna.connector.aggregator.order.received (the normalized event
// connector-hub publishes when a Zomato/Swiggy webhook arrives) and, for each,
// calls the app's OnAggregatorOrder (persist ExternalOrder + forward to ordering).
// Delivery is idempotent: pkg/eventbus/nats dedupes on Event.ID in process, and
// the app marks the event id processed + dedupes on (connector_id, external_ref)
// in the same tx as the insert, so a redelivery (even across restarts) is a no-op.
package nats

import (
	"context"
	"encoding/json"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/events"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/aggregators/internal/app"
)

// SubjectOrderReceived is the normalized aggregator-order subject connector-hub
// publishes on. It matches connectors.EventAggregatorOrderReceived.
const SubjectOrderReceived = "restorna.connector.aggregator.order.received"

// durable is the JetStream durable consumer name (one logical aggregators consumer).
const durable = "aggregators-order-received"

// orderData is the JSON shape of the normalized aggregator-order payload emitted
// by the connectors adapters (pkg/connectors/aggregator.go). Money is integer
// minor units + currency.
type orderData struct {
	ConnectorID  string `json:"connector_id"`
	ExternalRef  string `json:"external_ref"`
	RestaurantID string `json:"restaurant_id"`
	Status       string `json:"status"`
	Currency     string `json:"currency"`
	PlacedAt     string `json:"placed_at"`
	Items        []struct {
		Name       string `json:"name"`
		Qty        int32  `json:"qty"`
		PriceMinor int64  `json:"price_minor"`
		Currency   string `json:"currency"`
	} `json:"items"`
}

// Consumer wires the order-received subscription to the app use case.
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
// OnAggregatorOrder; a handler error nak's the message for redelivery.
func (c *Consumer) Run(ctx context.Context) error {
	return eventbus.Subscribe(ctx, c.natsURL, SubjectOrderReceived, durable, func(e events.Event) error {
		return c.handle(ctx, e)
	})
}

// handle decodes one aggregator-order event and drives OnAggregatorOrder. Mapping
// + tenancy wiring live here so the app stays transport-free.
func (c *Consumer) handle(ctx context.Context, e events.Event) error {
	var d orderData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		// Poison payload: ack/drop it so a malformed event cannot wedge the consumer.
		return nil
	}

	restaurantID := d.RestaurantID
	if restaurantID == "" {
		// Fall back to the trusted envelope tenant if the payload omitted it.
		restaurantID = e.TenantID
	}
	if restaurantID == "" {
		// No tenant to scope to — drop (cannot RLS-scope the write).
		return nil
	}

	// Scope the outgoing ordering call + the repo tx to this restaurant, derived
	// from the trusted event envelope/payload — never a request body.
	ctx = tenancy.With(ctx, tenancy.Scope{
		RestaurantID: restaurantID,
		Role:         commonv1.Role_ROLE_MANAGER,
	})

	items := make([]app.AggregatorOrderItem, 0, len(d.Items))
	for _, it := range d.Items {
		ccy := it.Currency
		if ccy == "" {
			ccy = d.Currency
		}
		items = append(items, app.AggregatorOrderItem{
			Name:       it.Name,
			Qty:        it.Qty,
			PriceMinor: it.PriceMinor,
			Currency:   ccy,
		})
	}

	if err := c.uc.OnAggregatorOrder(ctx, app.AggregatorOrder{
		EventID:      e.ID,
		RestaurantID: restaurantID,
		ConnectorID:  d.ConnectorID,
		ExternalRef:  d.ExternalRef,
		Status:       d.Status,
		PlacedAt:     d.PlacedAt,
		Items:        items,
	}); err != nil {
		return fmt.Errorf("aggregators consumer: order %s/%s: %w", d.ConnectorID, d.ExternalRef, err)
	}
	return nil
}
