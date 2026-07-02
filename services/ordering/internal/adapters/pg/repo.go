// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set (to the
// restaurant id) and RLS scopes rows. Outbox events are staged in the same tx
// (pkg/outbox.Stage). An order's lines live in a child table.
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/ordering/internal/domain"
	"github.com/restorna/platform/services/ordering/internal/ports"
)

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id=restaurant).
func (r *Repo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	return pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		return fn(&txAdapter{tx: tx, restaurantID: restaurantID})
	})
}

// GetOrder reads one order (+ lines) in its own tenant-scoped tx.
func (r *Repo) GetOrder(ctx context.Context, restaurantID, orderID string) (domain.Order, error) {
	var out domain.Order
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var err error
		out, err = getOrder(ctx, tx, orderID)
		return err
	})
	return out, err
}

// ListForRestaurant reads every order (+ lines) for the restaurant, newest first.
func (r *Repo) ListForRestaurant(ctx context.Context, restaurantID string) ([]domain.Order, error) {
	var out []domain.Order
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var err error
		out, err = listOrders(ctx, tx, restaurantID)
		return err
	})
	return out, err
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) InsertOrder(ctx context.Context, o domain.Order) error {
	if _, err := t.tx.Exec(ctx, `
		INSERT INTO orders (id, restaurant_id, table_id, subtotal_minor, currency, billed, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		o.ID, o.RestaurantID, o.TableID, o.Subtotal.Minor, o.Subtotal.Currency, o.Billed, o.CreatedAt); err != nil {
		return mapWrite(err)
	}
	for _, l := range o.Lines {
		if _, err := t.tx.Exec(ctx, `
			INSERT INTO order_lines (id, order_id, restaurant_id, menu_item_id, name, qty, unit_price_minor, currency, station)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			l.ID, o.ID, o.RestaurantID, l.MenuItemID, l.Name, l.Qty, l.UnitPrice.Minor, l.UnitPrice.Currency, l.Station); err != nil {
			return mapWrite(err)
		}
	}
	return nil
}

func (t *txAdapter) SetBilled(ctx context.Context, orderID string, billed bool) error {
	ct, err := t.tx.Exec(ctx, `UPDATE orders SET billed=$2 WHERE id=$1`, orderID, billed)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) SetTable(ctx context.Context, orderID, tableID string) error {
	ct, err := t.tx.Exec(ctx, `UPDATE orders SET table_id=$2 WHERE id=$1`, orderID, tableID)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) GetOrder(ctx context.Context, orderID string) (domain.Order, error) {
	return getOrder(ctx, t.tx, orderID)
}

func (t *txAdapter) ListForRestaurant(ctx context.Context, restaurantID string) ([]domain.Order, error) {
	return listOrders(ctx, t.tx, restaurantID)
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, tenantID string, data any) error {
	e := events.New(eventType, tenantID, data)
	return outbox.Stage(t.tx, e)
}

// --- shared query helpers (work over pgx.Tx) ---

// querier is the subset of pgx.Tx the read helpers need.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const selectOrder = `SELECT id, restaurant_id, table_id, subtotal_minor, currency, billed, created_at FROM orders`

func getOrder(ctx context.Context, q querier, orderID string) (domain.Order, error) {
	o, err := scanOrder(q.QueryRow(ctx, selectOrder+` WHERE id=$1`, orderID))
	if err != nil {
		return domain.Order{}, err
	}
	lines, err := loadLines(ctx, q, []string{o.ID})
	if err != nil {
		return domain.Order{}, err
	}
	o.Lines = lines[o.ID]
	return o, nil
}

func listOrders(ctx context.Context, q querier, restaurantID string) ([]domain.Order, error) {
	rows, err := q.Query(ctx, selectOrder+` WHERE restaurant_id=$1 ORDER BY created_at DESC, id DESC`, restaurantID)
	if err != nil {
		return nil, mapRead(err)
	}
	defer rows.Close()
	var orders []domain.Order
	ids := make([]string, 0)
	for rows.Next() {
		o, err := scanOrder(rows)
		if err != nil {
			return nil, err
		}
		orders = append(orders, o)
		ids = append(ids, o.ID)
	}
	if err := mapRead(rows.Err()); err != nil {
		return nil, err
	}
	byOrder, err := loadLines(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	for i := range orders {
		orders[i].Lines = byOrder[orders[i].ID]
	}
	return orders, nil
}

// loadLines fetches all lines for the given order ids, grouped by order id.
func loadLines(ctx context.Context, q querier, orderIDs []string) (map[string][]domain.Line, error) {
	out := map[string][]domain.Line{}
	if len(orderIDs) == 0 {
		return out, nil
	}
	rows, err := q.Query(ctx, `
		SELECT order_id, id, menu_item_id, name, qty, unit_price_minor, currency, station
		FROM order_lines WHERE order_id = ANY($1) ORDER BY id ASC`, orderIDs)
	if err != nil {
		return nil, mapRead(err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			orderID string
			l       domain.Line
			minor   int64
			ccy     string
		)
		if err := rows.Scan(&orderID, &l.ID, &l.MenuItemID, &l.Name, &l.Qty, &minor, &ccy, &l.Station); err != nil {
			return nil, mapRead(err)
		}
		l.UnitPrice = money.New(minor, ccy)
		out[orderID] = append(out[orderID], l)
	}
	return out, mapRead(rows.Err())
}

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanOrder(row scanner) (domain.Order, error) {
	var (
		o       domain.Order
		minor   int64
		ccy     string
		created time.Time
	)
	if err := row.Scan(&o.ID, &o.RestaurantID, &o.TableID, &minor, &ccy, &o.Billed, &created); err != nil {
		return domain.Order{}, mapRead(err)
	}
	o.Subtotal = money.New(minor, ccy)
	o.CreatedAt = created
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
