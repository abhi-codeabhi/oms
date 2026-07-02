package connectors

import (
	"encoding/json"
	"fmt"

	"github.com/restorna/platform/pkg/events"
)

// EventAggregatorOrderReceived is the normalized event type that connector-hub
// publishes when an aggregator (Zomato/Swiggy/mock) webhook carrying a new order
// is verified. The aggregators service subscribes to this subject, persists an
// ExternalOrder, and forwards it to OrderingService.PlaceOrder. Keeping the type
// here (next to the adapters that emit it) keeps producer and the contract in one
// place; the aggregators consumer references the same string.
const EventAggregatorOrderReceived = "restorna.connector.aggregator.order.received"

// aggregatorOrderWire is the tolerant JSON shape accepted from any aggregator
// order webhook. Providers differ in envelope, so the adapters normalize into
// this common form (the fields most providers include for a new order). Money is
// carried as integer minor units + currency (never floats) per CONVENTIONS.md.
type aggregatorOrderWire struct {
	OrderID    string `json:"order_id"`
	ExternalID string `json:"external_id"` // some providers use external_id
	ID         string `json:"id"`          // ...or a bare id
	Status     string `json:"status"`
	Currency   string `json:"currency"`
	PlacedAt   string `json:"placed_at"`
	Restaurant string `json:"restaurant_id"` // tenant hint if the provider echoes it
	Items      []struct {
		Name       string `json:"name"`
		Qty        int    `json:"qty"`
		Quantity   int    `json:"quantity"`    // alt spelling
		PriceMinor int64  `json:"price_minor"` // integer minor units
		Currency   string `json:"currency"`
	} `json:"items"`
}

// aggregatorOrderData is the stable JSON payload of
// EventAggregatorOrderReceived (data field). The aggregators consumer decodes
// exactly this shape.
type aggregatorOrderData struct {
	ConnectorID  string                    `json:"connector_id"`
	ExternalRef  string                    `json:"external_ref"`
	RestaurantID string                    `json:"restaurant_id,omitempty"`
	Status       string                    `json:"status"`
	Currency     string                    `json:"currency"`
	PlacedAt     string                    `json:"placed_at"`
	Items        []aggregatorOrderItemData `json:"items"`
}

// aggregatorOrderItemData is one normalized line on the event.
type aggregatorOrderItemData struct {
	Name       string `json:"name"`
	Qty        int    `json:"qty"`
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"`
}

// normalizeAggregatorOrder maps a verified provider order webhook to the shared
// restorna.connector.aggregator.order.received event. connectorID identifies the
// source aggregator (zomato|swiggy|mockagg). The external ref is resolved from
// whichever id field the provider used. Status defaults to "received".
func normalizeAggregatorOrder(connectorID string, body []byte) (events.Event, error) {
	var w aggregatorOrderWire
	if err := json.Unmarshal(body, &w); err != nil {
		return events.Event{}, fmt.Errorf("%s: decode order webhook: %w", connectorID, err)
	}
	ref := firstNonEmpty(w.OrderID, w.ExternalID, w.ID)
	if ref == "" {
		return events.Event{}, fmt.Errorf("%s: order webhook missing external ref", connectorID)
	}
	status := w.Status
	if status == "" {
		status = "received"
	}
	items := make([]aggregatorOrderItemData, 0, len(w.Items))
	for _, it := range w.Items {
		qty := it.Qty
		if qty == 0 {
			qty = it.Quantity
		}
		ccy := it.Currency
		if ccy == "" {
			ccy = w.Currency
		}
		items = append(items, aggregatorOrderItemData{
			Name:       it.Name,
			Qty:        qty,
			PriceMinor: it.PriceMinor,
			Currency:   ccy,
		})
	}
	data := aggregatorOrderData{
		ConnectorID:  connectorID,
		ExternalRef:  ref,
		RestaurantID: w.Restaurant,
		Status:       status,
		Currency:     w.Currency,
		PlacedAt:     w.PlacedAt,
		Items:        items,
	}
	// The tenant is resolved by connector-hub from the installation; the adapter
	// leaves TenantID empty and the hub fills it before publishing.
	return events.New(EventAggregatorOrderReceived, w.Restaurant, data), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
