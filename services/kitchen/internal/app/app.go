// Package app holds the kitchen use cases. It depends only on ports + domain. It
// orchestrates persistence (repo), event emission (outbox via Tx.StageEvent) and
// catalog resolution (MenuResolver) for the OrderPlaced choreography. The grpc
// adapter maps proto <-> these calls; the nats consumer drives OnOrderPlaced;
// tests drive everything with in-memory fakes.
package app

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/restorna/platform/services/kitchen/internal/domain"
	"github.com/restorna/platform/services/kitchen/internal/ports"
)

// Event types emitted by this service (CONVENTIONS.md naming:
// restorna.<context>.<aggregate>.<event>.v1).
const (
	EventTicketReady  = "restorna.kitchen.ticket.ready.v1"
	EventTicketServed = "restorna.kitchen.ticket.served.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo    ports.Repository
	catalog ports.MenuResolver
	now     Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now). catalog
// may be nil for command-only wiring (the grpc handler does not need it); the
// OrderPlaced consumer requires it.
func New(repo ports.Repository, catalog ports.MenuResolver, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, catalog: catalog, now: now}
}

// --- ReceiveTicket (manual; e.g. aggregator order) ---

// ReceiveItemInput is one already-resolved line (name + station) for a manual
// ticket. The OrderPlaced path resolves these from catalog first, then calls the
// same internal create path.
type ReceiveItemInput struct{ Name, Station string }

// ReceiveTicketInput is the validated input for fire-a-ticket.
type ReceiveTicketInput struct {
	OrderID string
	Table   string
	Items   []ReceiveItemInput
}

// ReceiveTicket fires a new ticket onto the board from already-resolved lines.
func (a *App) ReceiveTicket(ctx context.Context, restaurantID string, in ReceiveTicketInput) (domain.Ticket, error) {
	items := make([]domain.NewItemInput, 0, len(in.Items))
	for _, it := range in.Items {
		items = append(items, domain.NewItemInput{Name: it.Name, Station: it.Station})
	}
	return a.createTicket(ctx, restaurantID, in.OrderID, in.Table, items, "")
}

// createTicket builds + persists a ticket, marking eventID processed in the same
// tx when supplied (idempotent choreography). Shared by ReceiveTicket and
// OnOrderPlaced.
func (a *App) createTicket(ctx context.Context, restaurantID, orderID, table string, items []domain.NewItemInput, eventID string) (domain.Ticket, error) {
	t, err := domain.NewTicket(orderID, table, items, a.now())
	if err != nil {
		return domain.Ticket{}, err
	}
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if err := tx.Insert(ctx, t); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, restaurantID, eventID)
	})
	if err != nil {
		return domain.Ticket{}, err
	}
	return t, nil
}

// --- AdvanceItem ---

// AdvanceItem cycles one item new -> prep -> ready. When this makes the WHOLE
// ticket ready (and it was not already), the floor is notified with the same
// ticket.ready event as a bump, so the waiter sees "ready to serve" whether the
// cook bumped or finished item-by-item.
func (a *App) AdvanceItem(ctx context.Context, restaurantID, ticketID string, itemIndex int) (domain.Ticket, error) {
	var out domain.Ticket
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		t, err := tx.Get(ctx, ticketID)
		if err != nil {
			return err
		}
		wasReady := t.IsAllReady()
		if err := t.AdvanceItem(itemIndex); err != nil {
			return err
		}
		if err := tx.Update(ctx, t); err != nil {
			return err
		}
		if !wasReady && t.IsAllReady() {
			if err := tx.StageEvent(ctx, EventTicketReady, restaurantID, readyEvent(t)); err != nil {
				return err
			}
		}
		out = t
		return nil
	})
	if err != nil {
		return domain.Ticket{}, err
	}
	return out, nil
}

// --- Bump ---

// Bump marks the whole ticket ready. When it becomes fully ready (and was not
// already), ticket.ready is emitted. After a bump the ticket leaves the cook board
// (GetBoard excludes ready) and enters the waiter serve queue.
func (a *App) Bump(ctx context.Context, restaurantID, ticketID string) (domain.Ticket, error) {
	var out domain.Ticket
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		t, err := tx.Get(ctx, ticketID)
		if err != nil {
			return err
		}
		wasReady := t.IsAllReady()
		t.BumpAll()
		if err := tx.Update(ctx, t); err != nil {
			return err
		}
		if !wasReady && t.IsAllReady() {
			if err := tx.StageEvent(ctx, EventTicketReady, restaurantID, readyEvent(t)); err != nil {
				return err
			}
		}
		out = t
		return nil
	})
	if err != nil {
		return domain.Ticket{}, err
	}
	return out, nil
}

// --- Serve ---

// Serve marks ONE ready ticket delivered and emits ticket.served. Other tickets
// for the same table (still cooking, or another ready round) are untouched — this
// is why serving order #1 never serves order #2.
func (a *App) Serve(ctx context.Context, restaurantID, ticketID string) (domain.Ticket, error) {
	var out domain.Ticket
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		t, err := tx.Get(ctx, ticketID)
		if err != nil {
			return err
		}
		t.MarkServed()
		if err := tx.Update(ctx, t); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventTicketServed, restaurantID, servedEvent(t)); err != nil {
			return err
		}
		out = t
		return nil
	})
	if err != nil {
		return domain.Ticket{}, err
	}
	return out, nil
}

// --- queries ---

// GetBoard returns the active cook board: cooking tickets only, oldest first.
func (a *App) GetBoard(ctx context.Context, restaurantID string) ([]domain.Ticket, error) {
	all, err := a.repo.List(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	return domain.CookingBoard(all), nil
}

// ServeQueue returns the waiter serve queue: ready, unserved tickets, oldest first.
func (a *App) ServeQueue(ctx context.Context, restaurantID string) ([]domain.Ticket, error) {
	all, err := a.repo.List(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	return domain.ServeQueue(all), nil
}

// AllDay returns the all-day rail: counts of not-yet-ready items across live tickets.
func (a *App) AllDay(ctx context.Context, restaurantID string) (map[string]int32, error) {
	all, err := a.repo.List(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	return domain.AllDayCounts(all), nil
}

// --- choreography: OrderPlaced -> ReceiveTicket ---

// OrderPlacedLine is one line from the ordering.order.placed.v1 event payload.
type OrderPlacedLine struct {
	MenuItemID string
	Name       string
	Station    string
	Qty        int
}

// OrderPlaced is the parsed ordering.order.placed.v1 payload the consumer hands in.
type OrderPlaced struct {
	EventID      string
	OrderID      string
	RestaurantID string
	Table        string
	Lines        []OrderPlacedLine
}

// OnOrderPlaced is the idempotent handler for restorna.ordering.order.placed.v1.
// For each line it resolves the item NAME + station from CatalogService (falling
// back to any name/station already on the line), then fires one ticket for the
// order. Idempotency: if the event id was already processed the handler returns
// nil without creating a duplicate; otherwise the ticket insert + processed-event
// mark commit in one tx.
func (a *App) OnOrderPlaced(ctx context.Context, ev OrderPlaced) error {
	if ev.RestaurantID == "" {
		return fmt.Errorf("%w: order.placed missing restaurant_id", domain.ErrInvalid)
	}
	if len(ev.Lines) == 0 {
		return fmt.Errorf("%w: order.placed has no lines", domain.ErrInvalid)
	}

	items := make([]domain.NewItemInput, 0, len(ev.Lines))
	for _, ln := range ev.Lines {
		name, station := ln.Name, ln.Station
		// Resolve the canonical name/station from catalog when we have an item id
		// and a resolver. The catalog is the source of truth for kitchen routing.
		if a.catalog != nil && ln.MenuItemID != "" {
			if r, err := a.catalog.Resolve(ctx, ev.RestaurantID, ln.MenuItemID); err == nil {
				if strings.TrimSpace(r.Name) != "" {
					name = r.Name
				}
				if strings.TrimSpace(r.Station) != "" {
					station = r.Station
				}
			}
		}
		// One ticket item per unit so the cook sees real quantities on the rail.
		qty := ln.Qty
		if qty < 1 {
			qty = 1
		}
		for i := 0; i < qty; i++ {
			items = append(items, domain.NewItemInput{Name: name, Station: station})
		}
	}

	_, err := a.createTicket(ctx, ev.RestaurantID, ev.OrderID, ev.Table, items, ev.EventID)
	return err
}

// --- event payloads (kept small + stable; consumers project these) ---

func readyEvent(t domain.Ticket) map[string]any {
	return map[string]any{
		"ticket_id": t.ID,
		"order_id":  t.OrderID,
		"table":     t.Table,
	}
}

func servedEvent(t domain.Ticket) map[string]any {
	return map[string]any{
		"ticket_id": t.ID,
		"order_id":  t.OrderID,
		"table":     t.Table,
	}
}
