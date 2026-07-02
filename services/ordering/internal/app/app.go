// Package app holds the ordering use cases. It depends only on ports + domain. It
// builds orders (subtotal), persists them, stages the order.placed event (so
// kitchen + floor react via choreography), and serves table queries with tolerant
// matching. The grpc adapter maps proto <-> these calls; tests drive it with
// in-memory fakes.
//
// Ported from the proven Node ordering service: placeOrder + subtotal,
// listForTable (tolerant, unbilled by default), markBilled, relocate.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/ordering/internal/domain"
	"github.com/restorna/platform/services/ordering/internal/ports"
)

// EventOrderPlaced is emitted on PlaceOrder; kitchen (ticket) and floor (seat)
// consume it via events (CONVENTIONS.md naming: restorna.<ctx>.<aggregate>.<event>.v1).
const EventOrderPlaced = "restorna.ordering.order.placed.v1"

// Now is the clock; overridable in tests for deterministic timestamps.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo ports.Repository
	now  Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, now: now}
}

// --- PlaceOrder ---

// PlaceOrderInput is the validated input for placing a multi-round order.
type PlaceOrderInput struct {
	RestaurantID string
	TableID      string
	Items        []domain.NewLineInput
}

// PlaceOrder builds an order (computing the subtotal), persists it and stages the
// order.placed event in the SAME transaction (transactional outbox) so kitchen and
// floor react. restaurantID is the tenant key (RLS scope).
func (a *App) PlaceOrder(ctx context.Context, in PlaceOrderInput) (domain.Order, error) {
	o, err := domain.NewOrder(in.RestaurantID, in.TableID, in.Items, a.now())
	if err != nil {
		return domain.Order{}, err
	}
	err = a.repo.Atomic(ctx, in.RestaurantID, func(tx ports.Tx) error {
		if err := tx.InsertOrder(ctx, o); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventOrderPlaced, o.RestaurantID, orderPlacedEvent(o))
	})
	if err != nil {
		return domain.Order{}, err
	}
	return o, nil
}

// --- GetOrder ---

// GetOrder returns one order by id (RLS scopes to the caller's restaurant).
func (a *App) GetOrder(ctx context.Context, restaurantID, orderID string) (domain.Order, error) {
	if !ids.Valid(domain.PrefixOrder, orderID) {
		return domain.Order{}, fmt.Errorf("%w: order_id is invalid", domain.ErrInvalid)
	}
	return a.repo.GetOrder(ctx, restaurantID, orderID)
}

// --- ListForTable ---

// ListForTable returns every order for a table, matched tolerantly ("T7"/"7"/7).
// By default only the not-yet-billed orders are returned (what a final bill should
// aggregate); includeBilled returns the whole table history. (Proven Node rule.)
func (a *App) ListForTable(ctx context.Context, restaurantID, table string, includeBilled bool) ([]domain.Order, error) {
	all, err := a.repo.ListForRestaurant(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	want := domain.TableKey(table)
	out := make([]domain.Order, 0, len(all))
	for _, o := range all {
		if domain.TableKey(o.TableID) == want && (includeBilled || !o.Billed) {
			out = append(out, o)
		}
	}
	return out, nil
}

// --- MarkBilled ---

// MarkBilled flags the given orders as billed so a finalized bill never includes
// them twice. Missing/foreign ids are skipped; returns the count actually flipped.
func (a *App) MarkBilled(ctx context.Context, restaurantID string, orderIDs []string) (int, error) {
	if len(orderIDs) == 0 {
		return 0, nil
	}
	var count int
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		for _, id := range orderIDs {
			if !ids.Valid(domain.PrefixOrder, id) {
				continue
			}
			o, err := tx.GetOrder(ctx, id)
			if err != nil {
				continue // not found in this tenant — skip, don't fail the batch
			}
			if o.Billed {
				continue // already billed — idempotent
			}
			if err := tx.SetBilled(ctx, id, true); err != nil {
				return err
			}
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// --- Relocate ---

// Relocate moves every OPEN (unbilled) order from one table label to another, used
// when a waiter moves/swaps a party. Tables are matched tolerantly; returns the
// count moved. (Proven Node relocate rule.)
func (a *App) Relocate(ctx context.Context, restaurantID, fromTable, toTable string) (int, error) {
	if domain.TableKey(toTable) == "" {
		return 0, fmt.Errorf("%w: to_table is required", domain.ErrInvalid)
	}
	from := domain.TableKey(fromTable)
	if from == "" {
		return 0, fmt.Errorf("%w: from_table is required", domain.ErrInvalid)
	}

	var moved int
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		all, err := tx.ListForRestaurant(ctx, restaurantID)
		if err != nil {
			return err
		}
		for _, o := range all {
			if domain.TableKey(o.TableID) == from && !o.Billed {
				if err := tx.SetTable(ctx, o.ID, toTable); err != nil {
					return err
				}
				moved++
			}
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return moved, nil
}

// --- event payloads (kept small + stable; consumers project these) ---

// orderPlacedEvent is the order.placed payload kitchen + floor consume:
// { order_id, restaurant_id, table_id, lines[] }.
func orderPlacedEvent(o domain.Order) map[string]any {
	lines := make([]map[string]any, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, map[string]any{
			"line_id":      l.ID,
			"menu_item_id": l.MenuItemID,
			"name":         l.Name,
			"qty":          l.Qty,
			"station":      l.Station,
			"unit_price": map[string]any{
				"minor":    l.UnitPrice.Minor,
				"currency": l.UnitPrice.Currency,
			},
		})
	}
	return map[string]any{
		"order_id":      o.ID,
		"restaurant_id": o.RestaurantID,
		"table_id":      o.TableID,
		"subtotal": map[string]any{
			"minor":    o.Subtotal.Minor,
			"currency": o.Subtotal.Currency,
		},
		"lines":      lines,
		"created_at": o.CreatedAt.Format(time.RFC3339),
	}
}
