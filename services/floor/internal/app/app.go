// Package app holds the floor use cases. It depends only on ports + domain. It
// orchestrates persistence (repo), event emission (outbox via Tx.StageEvent), the
// DERIVED floor view (kitchen board + serve queue + open bills), the nudge engine
// (settings-driven config), and order relocation on move/swap. The grpc adapter
// maps proto <-> these calls; the nats consumers drive OnOrderPlaced /
// OnTicketServed; tests drive everything with in-memory fakes.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/restorna/platform/services/floor/internal/domain"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// Event types emitted by this service (CONVENTIONS.md naming:
// restorna.<context>.<aggregate>.<event>.v1).
const (
	EventFloorInitialized = "restorna.floor.floor.initialized.v1"
	EventTableSeated      = "restorna.floor.table.seated.v1"
	EventWaiterAssigned   = "restorna.floor.table.assigned.v1"
	EventTableMoved       = "restorna.floor.table.moved.v1"
)

// Now is the clock; overridable in tests for deterministic timers. It returns the
// current epoch milliseconds (nudge timers are epoch-ms).
type Now func() int64

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo     ports.Repository
	kitchen  ports.KitchenBoard
	billing  ports.BillingOpen
	settings ports.SettingsResolver
	ordering ports.OrderRelocator
	now      Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now in ms).
// The query/choreography ports (kitchen, billing, settings, ordering) may be nil
// for command-only wiring; GetFloor/GetNudges/Move require the relevant ones.
func New(repo ports.Repository, kitchen ports.KitchenBoard, billing ports.BillingOpen,
	settings ports.SettingsResolver, ordering ports.OrderRelocator, now Now) *App {
	if now == nil {
		now = func() int64 { return time.Now().UnixMilli() }
	}
	return &App{repo: repo, kitchen: kitchen, billing: billing, settings: settings, ordering: ordering, now: now}
}

// --- InitFloor ---

// InitFloor creates (replaces) the floor for the restaurant from a table list and
// emits floor.initialized. Returns ErrInvalid on duplicate/invalid table numbers.
func (a *App) InitFloor(ctx context.Context, restaurantID string, tableNumbers []int32) (domain.Floor, error) {
	f, err := domain.NewFloor(tableNumbers)
	if err != nil {
		return domain.Floor{}, err
	}
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventFloorInitialized, restaurantID, map[string]any{"tables": tableNumbers})
	})
	if err != nil {
		return domain.Floor{}, err
	}
	return f, nil
}

// --- GetFloor (DERIVED status) ---

// GetFloor returns the floor with each table's LIVE status DERIVED from its
// kitchen tickets + open bills (port of orchestration buildFloorView). It reads
// the stored doc, then calls KitchenService.GetBoard + ServeQueue and
// BillingService.ListOpen, groups them by table number, and overrides each table's
// status via domain.DeriveStatus (billing > ready > cooking > seated > free). The
// stored doc is never mutated — a stored status can't go stale.
func (a *App) GetFloor(ctx context.Context, restaurantID string) (domain.Floor, error) {
	f, err := a.repo.Get(ctx, restaurantID)
	if err != nil {
		return domain.Floor{}, err
	}

	loads := map[int32]*domain.TableLoad{}
	bumpLoad := func(label string, ready bool) {
		n := domain.TableNumber(label)
		if n == 0 {
			return
		}
		l := loads[n]
		if l == nil {
			l = &domain.TableLoad{}
			loads[n] = l
		}
		if ready {
			l.Ready++
		} else {
			l.Cooking++
		}
	}

	if a.kitchen != nil {
		cooking, err := a.kitchen.Board(ctx, restaurantID)
		if err != nil {
			return domain.Floor{}, err
		}
		for _, t := range cooking {
			bumpLoad(t.Table, false)
		}
		ready, err := a.kitchen.ServeQueue(ctx, restaurantID)
		if err != nil {
			return domain.Floor{}, err
		}
		for _, t := range ready {
			bumpLoad(t.Table, true)
		}
	}

	billed := map[int32]bool{}
	if a.billing != nil {
		open, err := a.billing.ListOpen(ctx, restaurantID)
		if err != nil {
			return domain.Floor{}, err
		}
		for _, b := range open {
			if n := domain.TableNumber(b.Table); n != 0 {
				billed[n] = true
			}
		}
	}

	for i := range f.Tables {
		t := f.Tables[i]
		var load domain.TableLoad
		if l := loads[t.N]; l != nil {
			load = *l
		}
		f.Tables[i].Status = domain.DeriveStatus(t, load, billed[t.N])
	}
	return f, nil
}

// --- SeatParty ---

// SeatParty seats an arriving party at a table, ensuring the table exists (so a
// host can seat any table) and arming the greet timer. Emits table.seated.
func (a *App) SeatParty(ctx context.Context, restaurantID string, n int32) (domain.Floor, error) {
	var out domain.Floor
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		f, err := loadOrNew(ctx, tx, restaurantID)
		if err != nil {
			return err
		}
		f.EnsureTable(n)
		if err := f.Seat(n, "", a.now()); err != nil {
			return err
		}
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		out = f
		return tx.StageEvent(ctx, EventTableSeated, restaurantID, map[string]any{"n": n})
	})
	if err != nil {
		return domain.Floor{}, err
	}
	return out, nil
}

// --- AssignWaiter (multi-table) ---

// AssignWaiter assigns one waiter to one or more tables in a single command and
// emits table.assigned. Returns ErrNotFound if any table is absent (all-or-nothing
// within the tx). An empty waiter id clears the assignment.
func (a *App) AssignWaiter(ctx context.Context, restaurantID string, ns []int32, waiterID string) (domain.Floor, error) {
	if len(ns) == 0 {
		return domain.Floor{}, fmt.Errorf("%w: at least one table is required", domain.ErrInvalid)
	}
	var out domain.Floor
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		f, err := tx.Get(ctx, restaurantID)
		if err != nil {
			return err
		}
		for _, n := range ns {
			if err := f.Assign(n, waiterID); err != nil {
				return err
			}
		}
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		out = f
		return tx.StageEvent(ctx, EventWaiterAssigned, restaurantID, map[string]any{"tables": ns, "waiter_id": waiterID})
	})
	if err != nil {
		return domain.Floor{}, err
	}
	return out, nil
}

// --- Move (move/swap) ---

// MoveResult is the outcome of a Move: the updated floor + the verb (moved|swapped).
type MoveResult struct {
	Floor domain.Floor
	Verb  string
}

// Move relocates a table's seat/order/waiter from src to dst (MOVE when dst is
// free, SWAP when busy), persists it, and ALSO calls OrderingService.Relocate so
// open orders follow the seat. The seat move + outbox event commit in one tx; the
// ordering Relocate is a best-effort cross-service call made after the local
// commit (a relocate failure is surfaced but the floor move stands). Kitchen
// ticket relocation needs a future kitchen.Relocate RPC (see README); the derived
// status will still be correct once tickets are re-tabled.
func (a *App) Move(ctx context.Context, restaurantID string, src, dst int32) (MoveResult, error) {
	var (
		out  domain.Floor
		verb string
	)
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		f, err := tx.Get(ctx, restaurantID)
		if err != nil {
			return err
		}
		v, err := f.MoveOrSwap(src, dst)
		if err != nil {
			return err
		}
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		out, verb = f, v
		return tx.StageEvent(ctx, EventTableMoved, restaurantID, map[string]any{"src": src, "dst": dst, "verb": v})
	})
	if err != nil {
		return MoveResult{}, err
	}

	// Relocate open orders so they follow the seat. For a MOVE this re-tables
	// src -> dst; for a SWAP the two table labels exchange orders, done as two
	// hops via a scratch label so neither clobbers the other.
	if a.ordering != nil {
		fromLabel := tableLabel(src)
		toLabel := tableLabel(dst)
		if verb == "swapped" {
			scratch := "MOVE_TMP"
			if _, err := a.ordering.Relocate(ctx, restaurantID, fromLabel, scratch); err != nil {
				return MoveResult{}, fmt.Errorf("relocate orders %s->tmp: %w", fromLabel, err)
			}
			if _, err := a.ordering.Relocate(ctx, restaurantID, toLabel, fromLabel); err != nil {
				return MoveResult{}, fmt.Errorf("relocate orders %s->%s: %w", toLabel, fromLabel, err)
			}
			if _, err := a.ordering.Relocate(ctx, restaurantID, scratch, toLabel); err != nil {
				return MoveResult{}, fmt.Errorf("relocate orders tmp->%s: %w", toLabel, err)
			}
		} else {
			if _, err := a.ordering.Relocate(ctx, restaurantID, fromLabel, toLabel); err != nil {
				return MoveResult{}, fmt.Errorf("relocate orders %s->%s: %w", fromLabel, toLabel, err)
			}
		}
	}
	return MoveResult{Floor: out, Verb: verb}, nil
}

// --- GetNudges ---

// GetNudges builds the active waiter prompts (greet/checkin/anything) from each
// table's timers vs the effective nudge config read from SettingsService
// (floor.nudge.greet_secs / checkin_secs / anything_secs). It does NOT mutate
// state. Oldest-waiting tables surface first.
func (a *App) GetNudges(ctx context.Context, restaurantID string) ([]domain.Nudge, error) {
	f, err := a.repo.Get(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	cfg := domain.DefaultNudgeConfig()
	if a.settings != nil {
		if c, err := a.settings.NudgeConfig(ctx, restaurantID); err == nil {
			cfg = c
		}
		// A settings failure falls back to defaults rather than blocking the floor.
	}
	return domain.BuildNudges(f.Tables, a.now(), cfg), nil
}

// --- AckNudge ---

// AckNudge records that a nudge was actioned: a greet ack sets greetedAt (silences
// the greet prompt), a checkin/anything ack sets lastCheckinAt (silences check-in
// and arms the anything timer). Returns ErrNotFound if the table is absent,
// ErrInvalid for an unknown type.
func (a *App) AckNudge(ctx context.Context, restaurantID string, n int32, typ string) error {
	return a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		f, err := tx.Get(ctx, restaurantID)
		if err != nil {
			return err
		}
		now := a.now()
		switch typ {
		case domain.NudgeGreet:
			if err := f.AckGreet(n, now); err != nil {
				return err
			}
		case domain.NudgeCheckin, domain.NudgeAnything:
			if err := f.AckCheckin(n, now); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown nudge type %q", domain.ErrInvalid, typ)
		}
		return tx.Save(ctx, restaurantID, f)
	})
}

// --- choreography handlers ---

// OnOrderPlaced is the idempotent handler for restorna.ordering.order.placed.v1:
// it ensures the table exists, seats it (setting seatedAt if unset — arming the
// greet timer), and records the order id. If the event id was already processed it
// returns nil without re-seating; otherwise the seat + processed-event mark commit
// in one tx.
func (a *App) OnOrderPlaced(ctx context.Context, restaurantID, eventID, tableLabelStr, orderID string) error {
	n := domain.TableNumber(tableLabelStr)
	if n == 0 {
		// No numeric table — nothing to seat; ack so the consumer doesn't wedge.
		return nil
	}
	return a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if eventID != "" {
			seen, err := tx.Seen(ctx, restaurantID, eventID)
			if err != nil {
				return err
			}
			if seen {
				return nil
			}
		}
		f, err := loadOrNew(ctx, tx, restaurantID)
		if err != nil {
			return err
		}
		f.EnsureTable(n)
		if err := f.Seat(n, orderID, a.now()); err != nil {
			return err
		}
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		if err := tx.MarkProcessed(ctx, restaurantID, eventID); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventTableSeated, restaurantID, map[string]any{"n": n})
	})
}

// OnTicketServed is the idempotent handler for restorna.kitchen.ticket.served.v1:
// it records the serve on the table (lastServedAt = now), arming the check-in
// nudge. No-op if the table is untracked. Dedupes on event id.
func (a *App) OnTicketServed(ctx context.Context, restaurantID, eventID, tableLabelStr string) error {
	n := domain.TableNumber(tableLabelStr)
	if n == 0 {
		return nil
	}
	return a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if eventID != "" {
			seen, err := tx.Seen(ctx, restaurantID, eventID)
			if err != nil {
				return err
			}
			if seen {
				return nil
			}
		}
		f, err := loadOrNew(ctx, tx, restaurantID)
		if err != nil {
			return err
		}
		// Ensure the table exists so an out-of-order served event still records.
		f.EnsureTable(n)
		f.MarkServed(n, a.now())
		if err := tx.Save(ctx, restaurantID, f); err != nil {
			return err
		}
		return tx.MarkProcessed(ctx, restaurantID, eventID)
	})
}

// --- helpers ---

// loadOrNew loads the floor doc or returns a fresh empty floor (used by the
// choreography handlers + SeatParty so a never-initialised floor still works).
func loadOrNew(ctx context.Context, tx ports.Tx, restaurantID string) (domain.Floor, error) {
	f, err := tx.Get(ctx, restaurantID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.Floor{ID: domain.FloorID}, nil
		}
		return domain.Floor{}, err
	}
	return f, nil
}

// tableLabel renders a numeric table as the "T<n>" label the OMS uses on orders.
func tableLabel(n int32) string {
	return "T" + itoa(int(n))
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
