// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. Outbox events + processed-event marks are staged in the
// same tx (pkg/outbox.Stage). Ticket items are stored as a JSONB column on the
// ticket row (the KDS reads/writes whole tickets; items never queried alone).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/kitchen/internal/domain"
	"github.com/restorna/platform/services/kitchen/internal/ports"
)

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id = restaurant).
func (r *Repo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	return pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return fn(&txAdapter{tx: tx, restaurantID: restaurantID})
	})
}

// List returns every live ticket for the restaurant, oldest first. The app derives
// board/queue/all-day views from this set in the domain.
func (r *Repo) List(ctx context.Context, restaurantID string) ([]domain.Ticket, error) {
	var out []domain.Ticket
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectTicket+` WHERE restaurant_id=$1 ORDER BY created_at, id`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTicket(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return mapRead(rows.Err())
	})
	return out, err
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) Get(ctx context.Context, ticketID string) (domain.Ticket, error) {
	return scanTicket(t.tx.QueryRow(ctx, selectTicket+` WHERE id=$1`, ticketID))
}

func (t *txAdapter) Insert(ctx context.Context, tk domain.Ticket) error {
	itemsJSON, err := json.Marshal(toItemRows(tk.Items))
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO tickets (id, restaurant_id, order_id, table_label, items, served, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		tk.ID, t.restaurantID, tk.OrderID, tk.Table, itemsJSON, tk.Served, tk.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) Update(ctx context.Context, tk domain.Ticket) error {
	itemsJSON, err := json.Marshal(toItemRows(tk.Items))
	if err != nil {
		return err
	}
	ct, err := t.tx.Exec(ctx, `
		UPDATE tickets SET items=$2, served=$3 WHERE id=$1`,
		tk.ID, itemsJSON, tk.Served)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, restaurantID string, data any) error {
	e := events.New(eventType, restaurantID, data)
	e.Source = "kitchen"
	return outbox.Stage(t.tx, e)
}

// MarkProcessed records a consumed event id (idempotent choreography) in this tx.
// ON CONFLICT DO NOTHING so a redelivered insert is harmless.
func (t *txAdapter) MarkProcessed(ctx context.Context, restaurantID, eventID string) error {
	if eventID == "" {
		return nil
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO processed_events (event_id, restaurant_id, processed_at)
		VALUES ($1, $2, now())
		ON CONFLICT (event_id) DO NOTHING`,
		eventID, restaurantID)
	return mapWrite(err)
}

// Seen reports whether eventID was already processed for the restaurant. Used by
// the consumer to short-circuit duplicates before opening a write tx.
func (r *Repo) Seen(ctx context.Context, restaurantID, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var seen bool
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, eventID).Scan(&seen)
	})
	return seen, err
}

var _ ports.ProcessedEvents = (*Repo)(nil)

// --- scan helpers ---

const selectTicket = `SELECT id, restaurant_id, order_id, table_label, items, served, created_at FROM tickets`

// itemRow is the JSONB shape of a ticket item (stable wire form for the items column).
type itemRow struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Station string `json:"station"`
	State   int32  `json:"state"`
}

func toItemRows(items []domain.TicketItem) []itemRow {
	out := make([]itemRow, 0, len(items))
	for _, it := range items {
		out = append(out, itemRow{ID: it.ID, Name: it.Name, Station: it.Station, State: int32(it.State)})
	}
	return out
}

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanTicket(row scanner) (domain.Ticket, error) {
	var (
		t        domain.Ticket
		rid      string
		itemsRaw []byte
		created  time.Time
	)
	if err := row.Scan(&t.ID, &rid, &t.OrderID, &t.Table, &itemsRaw, &t.Served, &created); err != nil {
		return domain.Ticket{}, mapRead(err)
	}
	var rows []itemRow
	if len(itemsRaw) > 0 {
		if err := json.Unmarshal(itemsRaw, &rows); err != nil {
			return domain.Ticket{}, err
		}
	}
	items := make([]domain.TicketItem, 0, len(rows))
	for _, r := range rows {
		items = append(items, domain.TicketItem{
			ID: r.ID, Name: r.Name, Station: r.Station, State: domain.ItemState(r.State),
		})
	}
	t.Items = items
	t.CreatedAt = created
	return t, nil
}

func mapRead(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}
