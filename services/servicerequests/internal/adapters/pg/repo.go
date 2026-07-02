// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. Outbox events are staged in the same tx
// (pkg/outbox.Stage). Two tables: `requests` (the aggregate) and `cooldowns` (the
// last acknowledge time per table+type, which gates re-raises).
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/servicerequests/internal/domain"
	"github.com/restorna/platform/services/servicerequests/internal/ports"
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

// List returns every request for the restaurant, oldest first. The app derives
// the open/escalation views from this set in the domain.
func (r *Repo) List(ctx context.Context, restaurantID string) ([]domain.Request, error) {
	var out []domain.Request
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectRequest+` WHERE restaurant_id=$1 ORDER BY created_at, id`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			req, err := scanRequest(rows)
			if err != nil {
				return err
			}
			out = append(out, req)
		}
		return mapRead(rows.Err())
	})
	return out, err
}

// LastAck returns the last acknowledge time for a table+type, or the zero time if
// none recorded (no active cooldown).
func (r *Repo) LastAck(ctx context.Context, restaurantID string, table int32, typ domain.Type) (time.Time, error) {
	var at time.Time
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT acked_at FROM cooldowns WHERE restaurant_id=$1 AND table_no=$2 AND type=$3`,
			restaurantID, table, string(typ))
		err := row.Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			at = time.Time{}
			return nil
		}
		return err
	})
	return at, err
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) Get(ctx context.Context, requestID string) (domain.Request, error) {
	return scanRequest(t.tx.QueryRow(ctx, selectRequest+` WHERE id=$1`, requestID))
}

func (t *txAdapter) Insert(ctx context.Context, r domain.Request) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO requests (id, restaurant_id, type, table_no, state, assigned_to, created_at, acked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		r.ID, t.restaurantID, string(r.Type), r.Table, string(r.State), r.AssignedTo,
		r.CreatedAt, nullTime(r.AckedAt))
	return mapWrite(err)
}

func (t *txAdapter) Update(ctx context.Context, r domain.Request) error {
	ct, err := t.tx.Exec(ctx, `
		UPDATE requests SET state=$2, assigned_to=$3, acked_at=$4 WHERE id=$1`,
		r.ID, string(r.State), r.AssignedTo, nullTime(r.AckedAt))
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// SetLastAck upserts the acknowledge time for a table+type (the cooldown anchor).
func (t *txAdapter) SetLastAck(ctx context.Context, restaurantID string, table int32, typ domain.Type, at time.Time) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO cooldowns (restaurant_id, table_no, type, acked_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (restaurant_id, table_no, type)
		DO UPDATE SET acked_at = EXCLUDED.acked_at`,
		restaurantID, table, string(typ), at)
	return mapWrite(err)
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, restaurantID string, data any) error {
	e := events.New(eventType, restaurantID, data)
	e.Source = "servicerequests"
	return outbox.Stage(t.tx, e)
}

// --- scan helpers ---

const selectRequest = `SELECT id, restaurant_id, type, table_no, state, assigned_to, created_at, acked_at FROM requests`

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanRequest(row scanner) (domain.Request, error) {
	var (
		r       domain.Request
		rid     string
		typ     string
		state   string
		created time.Time
		acked   *time.Time
	)
	if err := row.Scan(&r.ID, &rid, &typ, &r.Table, &state, &r.AssignedTo, &created, &acked); err != nil {
		return domain.Request{}, mapRead(err)
	}
	r.Type = domain.Type(typ)
	r.State = domain.State(state)
	r.CreatedAt = created
	if acked != nil {
		r.AckedAt = *acked
	}
	return r, nil
}

// nullTime maps the zero time to NULL so an un-acknowledged request stores NULL.
func nullTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
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
