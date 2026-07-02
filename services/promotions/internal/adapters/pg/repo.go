// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the outlet (restaurant_id). Coupons are keyed by (restaurant_id, code).
// Outbox events are staged in the same tx (pkg/outbox.Stage).
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
	"github.com/restorna/platform/services/promotions/internal/domain"
	"github.com/restorna/platform/services/promotions/internal/ports"
)

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id).
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

// UpsertCoupon creates or replaces a coupon keyed by (restaurant_id, code).
func (t *txAdapter) UpsertCoupon(ctx context.Context, c domain.Coupon) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO coupons
			(restaurant_id, code, type, value, min_order_minor, currency, category, active, starts_at, ends_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (restaurant_id, code) DO UPDATE SET
			type            = EXCLUDED.type,
			value           = EXCLUDED.value,
			min_order_minor = EXCLUDED.min_order_minor,
			currency        = EXCLUDED.currency,
			category        = EXCLUDED.category,
			active          = EXCLUDED.active,
			starts_at       = EXCLUDED.starts_at,
			ends_at         = EXCLUDED.ends_at`,
		t.restaurantID, c.Code, c.Type, c.Value, c.MinOrder.Minor, c.MinOrder.Currency,
		c.Category, c.Active, c.StartsAt, c.EndsAt)
	return mapWrite(err)
}

// GetCoupon loads a coupon by code within the tx's restaurant.
func (t *txAdapter) GetCoupon(ctx context.Context, code string) (domain.Coupon, error) {
	return scanCoupon(t.tx.QueryRow(ctx, selectCoupon+` WHERE restaurant_id=$1 AND code=$2`, t.restaurantID, code))
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, tenantID string, data any) error {
	e := events.New(eventType, tenantID, data)
	return outbox.Stage(t.tx, e)
}

// ListCoupons returns every coupon for the restaurant (own tenant-scoped tx).
func (r *Repo) ListCoupons(ctx context.Context, restaurantID string) ([]domain.Coupon, error) {
	var out []domain.Coupon
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectCoupon+` WHERE restaurant_id=$1 ORDER BY code`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCoupon(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return mapRead(rows.Err())
	})
	return out, err
}

// --- scan helpers ---

const selectCoupon = `SELECT code, type, value, min_order_minor, currency, category, active, starts_at, ends_at FROM coupons`

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanCoupon(row scanner) (domain.Coupon, error) {
	var (
		c        domain.Coupon
		minMinor int64
		currency string
		starts   *time.Time
		ends     *time.Time
	)
	if err := row.Scan(&c.Code, &c.Type, &c.Value, &minMinor, &currency, &c.Category, &c.Active, &starts, &ends); err != nil {
		return domain.Coupon{}, mapRead(err)
	}
	c.MinOrder = money.New(minMinor, currency)
	c.StartsAt = utcPtr(starts)
	c.EndsAt = utcPtr(ends)
	return c, nil
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
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

func mapWrite(err error) error {
	if err == nil {
		return nil
	}
	if isUniqueViolation(err) {
		return errAlreadyExists
	}
	return err
}
