// Package app holds the aggregators use cases. It depends only on ports + domain.
// It orchestrates: PushMenu (pull catalog -> serialize -> resolve connector ->
// push), the inbound-order choreography (persist ExternalOrder -> forward to
// ordering.PlaceOrder -> emit order.received), AckExternalOrder (push status
// upstream), and ListExternalOrders. The grpc adapter maps proto <-> these calls;
// the nats consumer drives OnAggregatorOrder; tests drive everything with fakes.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/aggregators/internal/domain"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

// EventOrderReceived is emitted (outbox) after an ingested aggregator order is
// persisted + forwarded to ordering (CONVENTIONS.md naming).
const EventOrderReceived = "restorna.aggregators.order.received.v1"

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer). hub/catalog/
// ordering may be nil for wiring that does not need them (e.g. a query-only path),
// but PushMenu needs catalog+hub and the order consumer needs ordering.
type App struct {
	repo     ports.Repository
	hub      ports.ConnectorHub
	catalog  ports.Catalog
	ordering ports.Ordering
	now      Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, hub ports.ConnectorHub, catalog ports.Catalog, ordering ports.Ordering, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, hub: hub, catalog: catalog, ordering: ordering, now: now}
}

// --- PushMenu ---

// PushMenuResult reports the outcome of a menu push.
type PushMenuResult struct {
	OK    bool
	Items int32
}

// PushMenu fetches the current menu from CatalogService, serializes it, resolves
// the active aggregator via connector-hub, and pushes it to that adapter.
// preferConnectorID (from the RPC) selects a specific aggregator when set.
func (a *App) PushMenu(ctx context.Context, restaurantID, preferConnectorID string) (PushMenuResult, error) {
	if restaurantID == "" {
		return PushMenuResult{}, fmt.Errorf("%w: restaurant_id is required", domain.ErrInvalid)
	}
	if a.catalog == nil || a.hub == nil {
		return PushMenuResult{}, fmt.Errorf("%w: menu push not configured", domain.ErrInvalid)
	}

	items, err := a.catalog.ListAllItems(ctx, restaurantID)
	if err != nil {
		return PushMenuResult{}, fmt.Errorf("fetch catalog: %w", err)
	}

	rc, err := a.hub.Resolve(ctx, restaurantID, preferConnectorID)
	if err != nil {
		return PushMenuResult{}, fmt.Errorf("resolve aggregator: %w", err)
	}

	menuJSON, err := serializeMenu(items)
	if err != nil {
		return PushMenuResult{}, fmt.Errorf("serialize menu: %w", err)
	}

	accepted, err := a.hub.PushMenu(ctx, rc, menuJSON)
	if err != nil {
		return PushMenuResult{}, fmt.Errorf("push menu to %s: %w", rc.ConnectorID, err)
	}
	return PushMenuResult{OK: true, Items: int32(accepted)}, nil
}

// menuWire is the stable serialized menu the aggregator adapters count/push.
type menuWire struct {
	RestaurantID string         `json:"restaurant_id"`
	Items        []menuItemWire `json:"items"`
}

type menuItemWire struct {
	ID          string `json:"id"`
	CategoryID  string `json:"category_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Currency    string `json:"currency"`
	Veg         bool   `json:"veg"`
	Available   bool   `json:"available"`
	Station     string `json:"station"`
}

func serializeMenu(items []ports.MenuItem) ([]byte, error) {
	w := menuWire{Items: make([]menuItemWire, 0, len(items))}
	for _, it := range items {
		w.Items = append(w.Items, menuItemWire{
			ID:          it.ID,
			CategoryID:  it.CategoryID,
			Name:        it.Name,
			Description: it.Description,
			PriceMinor:  it.PriceMinor,
			Currency:    it.Currency,
			Veg:         it.Veg,
			Available:   it.Available,
			Station:     it.Station,
		})
	}
	return json.Marshal(w)
}

// --- ListExternalOrders ---

// ListExternalOrders returns persisted external orders for the restaurant,
// optionally filtered by connector id and/or status.
func (a *App) ListExternalOrders(ctx context.Context, restaurantID, connectorID, status string) ([]domain.ExternalOrder, error) {
	if restaurantID == "" {
		return nil, fmt.Errorf("%w: restaurant_id is required", domain.ErrInvalid)
	}
	orders, err := a.repo.List(ctx, restaurantID, connectorID, status)
	if err != nil {
		return nil, err
	}
	domain.SortByCreatedAt(orders)
	return orders, nil
}

// --- AckExternalOrder ---

// AckExternalOrder updates an external order's status (accept/reject/update) and
// pushes that status upstream to the aggregator. The status change is persisted;
// the upstream ack is best-effort against the connector adapter (a transient
// aggregator error does not lose the local state — status is already stored).
func (a *App) AckExternalOrder(ctx context.Context, restaurantID, externalOrderID, status string) (domain.ExternalOrder, error) {
	if restaurantID == "" {
		return domain.ExternalOrder{}, fmt.Errorf("%w: restaurant_id is required", domain.ErrInvalid)
	}
	var out domain.ExternalOrder
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		o, err := tx.Get(ctx, externalOrderID)
		if err != nil {
			return err
		}
		if err := o.SetStatus(status); err != nil {
			return err
		}
		if err := tx.Update(ctx, o); err != nil {
			return err
		}
		out = o
		return nil
	})
	if err != nil {
		return domain.ExternalOrder{}, err
	}
	return out, nil
}

// --- choreography: aggregator.order.received -> persist + forward to ordering ---

// AggregatorOrderItem is one normalized line from the ingested order event.
type AggregatorOrderItem struct {
	Name       string
	Qty        int32
	PriceMinor int64
	Currency   string
}

// AggregatorOrder is the parsed normalized aggregator-order event the consumer
// hands in (restorna.connector.aggregator.order.received).
type AggregatorOrder struct {
	EventID      string
	RestaurantID string
	ConnectorID  string
	ExternalRef  string
	Status       string
	PlacedAt     string
	Items        []AggregatorOrderItem
}

// OnAggregatorOrder is the idempotent handler for an ingested aggregator order.
// It persists an ExternalOrder, stages the order.received event + marks the event
// processed in ONE tx, then forwards the order to OrderingService.PlaceOrder at a
// synthetic table ("AGG-<ref>") so it hits the kitchen like any dine-in order.
//
// Idempotency is twofold: (1) the event id is deduped in processed_events, and
// (2) the (connector_id, external_ref) pair is unique — a redelivery (same event
// id, or the same order via a different event) is a no-op and is NOT forwarded to
// ordering a second time.
func (a *App) OnAggregatorOrder(ctx context.Context, ev AggregatorOrder) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: aggregator order missing restaurant_id", domain.ErrInvalid)
	}
	if len(ev.Items) == 0 {
		return fmt.Errorf("%w: aggregator order has no items", domain.ErrInvalid)
	}

	items := make([]domain.Item, 0, len(ev.Items))
	for _, it := range ev.Items {
		ccy := it.Currency
		if ccy == "" {
			ccy = "INR"
		}
		items = append(items, domain.Item{
			Name:  it.Name,
			Qty:   it.Qty,
			Price: money.New(it.PriceMinor, ccy),
		})
	}

	order, err := domain.NewExternalOrder(domain.NewExternalOrderInput{
		RestaurantID: ev.RestaurantID,
		ConnectorID:  ev.ConnectorID,
		ExternalRef:  ev.ExternalRef,
		Status:       ev.Status,
		Items:        items,
		PlacedAt:     ev.PlacedAt,
	}, a.now())
	if err != nil {
		return err
	}

	// Persist + stage event + mark processed, all-or-nothing. If the order already
	// exists (by event id OR by connector+ref) we skip both the insert and the
	// downstream forward — created stays false.
	var created bool
	err = a.repo.Atomic(ctx, ev.RestaurantID, func(tx ports.Tx) error {
		if ev.EventID != "" {
			if seen, err := tx.Seen(ctx, ev.RestaurantID, ev.EventID); err != nil {
				return err
			} else if seen {
				return nil
			}
		}
		if _, err := tx.GetByRef(ctx, ev.ConnectorID, ev.ExternalRef); err == nil {
			// Already ingested this aggregator order under a prior event — dedupe.
			return tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID)
		} else if err != nil && err != domain.ErrNotFound {
			return err
		}
		if err := tx.Insert(ctx, order); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventOrderReceived, ev.RestaurantID, orderReceivedEvent(order)); err != nil {
			return err
		}
		if err := tx.MarkProcessed(ctx, ev.RestaurantID, ev.EventID); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return err
	}
	if !created {
		return nil // duplicate: do not double-forward to ordering
	}

	// Forward into ordering so the order flows to the kitchen like a dine-in order.
	// The synthetic table label carries the aggregator ref for traceability.
	if a.ordering != nil {
		lines := make([]ports.OrderLine, 0, len(order.Items))
		for _, it := range order.Items {
			lines = append(lines, ports.OrderLine{
				Name:       it.Name,
				Qty:        it.Qty,
				PriceMinor: it.Price.Minor,
				Currency:   it.Price.Currency,
			})
		}
		if _, err := a.ordering.PlaceOrder(ctx, ev.RestaurantID, order.SyntheticTable(), lines); err != nil {
			return fmt.Errorf("forward to ordering: %w", err)
		}
	}
	return nil
}

// orderReceivedEvent is the stable payload for restorna.aggregators.order.received.v1.
func orderReceivedEvent(o domain.ExternalOrder) map[string]any {
	return map[string]any{
		"external_order_id": o.ID,
		"restaurant_id":     o.RestaurantID,
		"connector_id":      o.ConnectorID,
		"external_ref":      o.ExternalRef,
		"status":            string(o.Status),
		"table":             o.SyntheticTable(),
	}
}
