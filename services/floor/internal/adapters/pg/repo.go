// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. The floor is ONE row per restaurant (id = 'floor') with
// its tables stored as a JSONB column — the floor is read/written wholesale and
// the table set is small. Outbox events + processed-event marks are staged in the
// same tx (pkg/outbox.Stage).
package pg

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/floor/internal/domain"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Get loads the restaurant's floor doc, or domain.ErrNotFound if absent.
func (r *Repo) Get(ctx context.Context, restaurantID string) (domain.Floor, error) {
	var f domain.Floor
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var gErr error
		f, gErr = getFloor(ctx, tx, restaurantID)
		return gErr
	})
	return f, err
}

// Save upserts the restaurant's floor doc.
func (r *Repo) Save(ctx context.Context, restaurantID string, f domain.Floor) error {
	return pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return saveFloor(ctx, tx, restaurantID, f)
	})
}

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id = restaurant).
func (r *Repo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	return pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return fn(&txAdapter{tx: tx, restaurantID: restaurantID})
	})
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) Get(ctx context.Context, restaurantID string) (domain.Floor, error) {
	return getFloor(ctx, t.tx, restaurantID)
}

func (t *txAdapter) Save(ctx context.Context, restaurantID string, f domain.Floor) error {
	return saveFloor(ctx, t.tx, restaurantID, f)
}

func (t *txAdapter) StageEvent(ctx context.Context, eventType, restaurantID string, data any) error {
	e := events.New(eventType, restaurantID, data)
	e.Source = "floor"
	return outbox.Stage(t.tx, e)
}

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

func (t *txAdapter) Seen(ctx context.Context, restaurantID, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var seen bool
	err := t.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, eventID).Scan(&seen)
	return seen, err
}

// --- shared SQL ---

// tableRow is the JSONB shape of a floor table (stable wire form for the column).
type tableRow struct {
	N             int32  `json:"n"`
	Status        string `json:"status"`
	Order         string `json:"order"`
	WaiterID      string `json:"waiter_id"`
	SeatedAt      int64  `json:"seated_at"`
	GreetedAt     int64  `json:"greeted_at"`
	LastServedAt  int64  `json:"last_served_at"`
	LastCheckinAt int64  `json:"last_checkin_at"`
}

func getFloor(ctx context.Context, tx pgx.Tx, restaurantID string) (domain.Floor, error) {
	var raw []byte
	err := tx.QueryRow(ctx,
		`SELECT tables FROM floors WHERE restaurant_id=$1`, restaurantID).Scan(&raw)
	if err != nil {
		return domain.Floor{}, mapRead(err)
	}
	var rows []tableRow
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			return domain.Floor{}, err
		}
	}
	tables := make([]domain.Table, 0, len(rows))
	for _, r := range rows {
		tables = append(tables, domain.Table{
			N:             r.N,
			Status:        r.Status,
			Order:         r.Order,
			WaiterID:      r.WaiterID,
			SeatedAt:      r.SeatedAt,
			GreetedAt:     r.GreetedAt,
			LastServedAt:  r.LastServedAt,
			LastCheckinAt: r.LastCheckinAt,
		})
	}
	return domain.Floor{ID: domain.FloorID, Tables: tables}, nil
}

func saveFloor(ctx context.Context, tx pgx.Tx, restaurantID string, f domain.Floor) error {
	rows := make([]tableRow, 0, len(f.Tables))
	for _, t := range f.Tables {
		rows = append(rows, tableRow{
			N:             t.N,
			Status:        t.Status,
			Order:         t.Order,
			WaiterID:      t.WaiterID,
			SeatedAt:      t.SeatedAt,
			GreetedAt:     t.GreetedAt,
			LastServedAt:  t.LastServedAt,
			LastCheckinAt: t.LastCheckinAt,
		})
	}
	raw, err := json.Marshal(rows)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO floors (restaurant_id, tables, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (restaurant_id) DO UPDATE SET tables=EXCLUDED.tables, updated_at=now()`,
		restaurantID, raw)
	return mapWrite(err)
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
