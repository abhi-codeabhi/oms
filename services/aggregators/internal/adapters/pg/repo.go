// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. Outbox events + processed-event marks are staged in the
// same tx (pkg/outbox.Stage). Order items are stored as a JSONB column on the
// external_orders row (read/written as a whole order; items never queried alone).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/aggregators/internal/domain"
	"github.com/restorna/platform/services/aggregators/internal/ports"
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

// Get loads one external order by id (RLS-scoped).
func (r *Repo) Get(ctx context.Context, restaurantID, id string) (domain.ExternalOrder, error) {
	var o domain.ExternalOrder
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		got, err := scanOrder(tx.QueryRow(ctx, selectOrder+` WHERE id=$1`, id))
		if err != nil {
			return err
		}
		o = got
		return nil
	})
	return o, err
}

// List returns external orders for the restaurant, optionally filtered by
// connector id and/or status (empty = no filter). Oldest first.
func (r *Repo) List(ctx context.Context, restaurantID, connectorID, status string) ([]domain.ExternalOrder, error) {
	var out []domain.ExternalOrder
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectOrder+`
			WHERE restaurant_id=$1
			  AND ($2 = '' OR connector_id = $2)
			  AND ($3 = '' OR status = $3)
			ORDER BY created_at, id`,
			restaurantID, connectorID, status)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOrder(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
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

func (t *txAdapter) Get(ctx context.Context, id string) (domain.ExternalOrder, error) {
	return scanOrder(t.tx.QueryRow(ctx, selectOrder+` WHERE id=$1`, id))
}

func (t *txAdapter) GetByRef(ctx context.Context, connectorID, externalRef string) (domain.ExternalOrder, error) {
	return scanOrder(t.tx.QueryRow(ctx,
		selectOrder+` WHERE connector_id=$1 AND external_ref=$2`, connectorID, externalRef))
}

func (t *txAdapter) Insert(ctx context.Context, o domain.ExternalOrder) error {
	itemsJSON, err := json.Marshal(toItemRows(o.Items))
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO external_orders
			(id, restaurant_id, connector_id, external_ref, status, items, placed_at, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		o.ID, t.restaurantID, o.ConnectorID, o.ExternalRef, string(o.Status), itemsJSON, o.PlacedAt, o.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) Update(ctx context.Context, o domain.ExternalOrder) error {
	ct, err := t.tx.Exec(ctx, `UPDATE external_orders SET status=$2 WHERE id=$1`,
		o.ID, string(o.Status))
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
	e.Source = "aggregators"
	return outbox.Stage(t.tx, e)
}

// MarkProcessed records a consumed event id (idempotent choreography) in this tx.
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

// Seen reports whether eventID was already processed for the restaurant, in this tx.
func (t *txAdapter) Seen(ctx context.Context, restaurantID, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	var seen bool
	err := t.tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM processed_events WHERE event_id=$1)`, eventID).Scan(&seen)
	return seen, err
}

// --- scan helpers ---

const selectOrder = `SELECT id, restaurant_id, connector_id, external_ref, status, items, placed_at, created_at FROM external_orders`

// itemRow is the JSONB shape of an order line (stable wire form for the items column).
type itemRow struct {
	Name          string `json:"name"`
	Qty           int32  `json:"qty"`
	PriceMinor    int64  `json:"price_minor"`
	PriceCurrency string `json:"price_currency"`
}

func toItemRows(items []domain.Item) []itemRow {
	out := make([]itemRow, 0, len(items))
	for _, it := range items {
		out = append(out, itemRow{
			Name:          it.Name,
			Qty:           it.Qty,
			PriceMinor:    it.Price.Minor,
			PriceCurrency: it.Price.Currency,
		})
	}
	return out
}

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (domain.ExternalOrder, error) {
	var (
		o        domain.ExternalOrder
		status   string
		itemsRaw []byte
		created  time.Time
	)
	if err := row.Scan(&o.ID, &o.RestaurantID, &o.ConnectorID, &o.ExternalRef, &status, &itemsRaw, &o.PlacedAt, &created); err != nil {
		return domain.ExternalOrder{}, mapRead(err)
	}
	o.Status = domain.Status(status)
	o.CreatedAt = created
	var rows []itemRow
	if len(itemsRaw) > 0 {
		if err := json.Unmarshal(itemsRaw, &rows); err != nil {
			return domain.ExternalOrder{}, err
		}
	}
	items := make([]domain.Item, 0, len(rows))
	for _, r := range rows {
		items = append(items, domain.Item{
			Name:  r.Name,
			Qty:   r.Qty,
			Price: money.New(r.PriceMinor, r.PriceCurrency),
		})
	}
	o.Items = items
	return o, nil
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
