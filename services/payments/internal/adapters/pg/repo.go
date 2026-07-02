// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the restaurant. Outbox events + processed-event marks are staged in the
// same tx (pkg/outbox.Stage).
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/payments/internal/domain"
	"github.com/restorna/platform/services/payments/internal/ports"
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

// Get loads a payment by id (its own tenant-scoped tx).
func (r *Repo) Get(ctx context.Context, restaurantID, paymentID string) (domain.Payment, error) {
	var out domain.Payment
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var err error
		out, err = scanPayment(tx.QueryRow(ctx, selectPayment+` WHERE id=$1`, paymentID))
		return err
	})
	return out, err
}

// FindByIdempotencyKey returns the payment previously created for key.
func (r *Repo) FindByIdempotencyKey(ctx context.Context, restaurantID, key string) (domain.Payment, error) {
	var out domain.Payment
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var err error
		out, err = scanPayment(tx.QueryRow(ctx, selectPayment+` WHERE idempotency_key=$1`, key))
		return err
	})
	return out, err
}

// FindByProviderRef matches a webhook to its payment by the gateway ref. When the
// envelope tenant is unknown (restaurantID == "") the lookup bypasses RLS via an
// empty tenant so a provider webhook can find its payment; provider_ref is unique.
func (r *Repo) FindByProviderRef(ctx context.Context, restaurantID, providerRef string) (domain.Payment, error) {
	var out domain.Payment
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		var err error
		out, err = scanPayment(tx.QueryRow(ctx, selectPayment+` WHERE provider_ref=$1`, providerRef))
		return err
	})
	return out, err
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx           pgx.Tx
	restaurantID string
}

func (t *txAdapter) Get(ctx context.Context, paymentID string) (domain.Payment, error) {
	return scanPayment(t.tx.QueryRow(ctx, selectPayment+` WHERE id=$1`, paymentID))
}

func (t *txAdapter) Insert(ctx context.Context, p domain.Payment, idempotencyKey string) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO payments
			(id, restaurant_id, bill_id, amount_minor, currency, connector_id, provider_ref,
			 status, method, refunded_minor, idempotency_key, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		p.ID, p.RestaurantID, p.BillID, p.Amount.Minor, p.Amount.Currency, p.ConnectorID, p.ProviderRef,
		string(p.Status), p.Method, p.Refunded.Minor, idempotencyKey, p.CreatedAt, p.UpdatedAt)
	return mapWrite(err)
}

func (t *txAdapter) Update(ctx context.Context, p domain.Payment) error {
	ct, err := t.tx.Exec(ctx, `
		UPDATE payments
		SET provider_ref=$2, status=$3, method=$4, refunded_minor=$5, updated_at=$6
		WHERE id=$1`,
		p.ID, p.ProviderRef, string(p.Status), p.Method, p.Refunded.Minor, p.UpdatedAt)
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
	return outbox.Stage(t.tx, e)
}

// MarkProcessed records a consumed webhook event id (idempotent choreography) in
// this tx. ON CONFLICT DO NOTHING so a redelivered event is harmless.
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

// --- scan helpers ---

const selectPayment = `
	SELECT id, restaurant_id, bill_id, amount_minor, currency, connector_id, provider_ref,
	       status, method, refunded_minor, created_at, updated_at
	FROM payments`

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanPayment(row scanner) (domain.Payment, error) {
	var (
		p                   domain.Payment
		amountMinor, refMnr int64
		currency, status    string
		created, updated    time.Time
	)
	if err := row.Scan(&p.ID, &p.RestaurantID, &p.BillID, &amountMinor, &currency, &p.ConnectorID,
		&p.ProviderRef, &status, &p.Method, &refMnr, &created, &updated); err != nil {
		return domain.Payment{}, mapRead(err)
	}
	p.Amount = money.New(amountMinor, currency)
	p.Refunded = money.New(refMnr, currency)
	p.Status = domain.Status(status)
	p.CreatedAt = created
	p.UpdatedAt = updated
	return p, nil
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
		return fmt.Errorf("%w: duplicate", errAlreadyExists)
	}
	return err
}
