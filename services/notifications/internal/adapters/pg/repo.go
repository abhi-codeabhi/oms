// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// owner-scoped operation runs inside pkg/pg.WithTenant so app.tenant_id is set and
// RLS scopes rows to the owner. Outbox events are staged in the same tx
// (pkg/outbox.Stage). Webhook lookups (by provider ref, processed events) run under
// the empty tenant since a provider callback isn't tied to a JWT.
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
	"github.com/restorna/platform/services/notifications/internal/domain"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

// DefaultTemplateOwner is the sentinel owner id holding the platform's built-in
// template copy (OTP, staff invite, etc.). GetTemplate falls back to it when an
// owner has not overridden a template, so identity/staff flows work out of the box.
const DefaultTemplateOwner = "own_platform"

// Repo implements ports.Repository over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repository = (*Repo)(nil)

// New builds a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Atomic runs fn in a tenant-scoped transaction (RLS via app.tenant_id).
func (r *Repo) Atomic(ctx context.Context, ownerID string, fn func(ports.Tx) error) error {
	return pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		return fn(&txAdapter{tx: tx})
	})
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct{ tx pgx.Tx }

func (t *txAdapter) InsertMessage(ctx context.Context, m domain.Message) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO messages
			(id, owner_id, restaurant_id, channel, recipient, template_id, vars,
			 subject, body, status, provider_id, provider_ref, idempotency_key, error,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		m.ID, m.OwnerID, nullStr(m.RestaurantID), string(m.Channel), m.To, m.TemplateID, m.Vars,
		m.Subject, m.Body, string(m.Status), nullStr(m.ProviderID), nullStr(m.ProviderRef),
		nullStr(m.IdempotencyKey), nullStr(m.Error), m.CreatedAt, m.UpdatedAt)
	return err
}

func (t *txAdapter) UpdateMessage(ctx context.Context, m domain.Message) error {
	ct, err := t.tx.Exec(ctx, `
		UPDATE messages
		SET status=$2, provider_id=$3, provider_ref=$4, error=$5, updated_at=$6
		WHERE id=$1`,
		m.ID, string(m.Status), nullStr(m.ProviderID), nullStr(m.ProviderRef), nullStr(m.Error), m.UpdatedAt)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) StageEvent(ctx context.Context, eventType, ownerID string, data any) error {
	return outbox.Stage(t.tx, events.New(eventType, ownerID, data))
}

// --- read methods (each opens its own tenant-scoped tx) ---

func (r *Repo) GetMessage(ctx context.Context, ownerID, messageID string) (domain.Message, error) {
	var m domain.Message
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var err error
		m, err = scanMessage(tx.QueryRow(ctx, selectMessage+` WHERE id=$1`, messageID))
		return err
	})
	return m, err
}

func (r *Repo) FindByIdempotencyKey(ctx context.Context, ownerID, key string) (domain.Message, bool, error) {
	var (
		m     domain.Message
		found bool
	)
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		mm, err := scanMessage(tx.QueryRow(ctx, selectMessage+` WHERE idempotency_key=$1`, key))
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		m, found = mm, true
		return nil
	})
	return m, found, err
}

// FindByProviderRef runs under the empty tenant (webhook context) since a provider
// callback is not tied to a JWT. RLS on messages allows the empty-tenant admin path.
func (r *Repo) FindByProviderRef(ctx context.Context, providerID, providerRef string) (domain.Message, bool, error) {
	var (
		m     domain.Message
		found bool
	)
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		mm, err := scanMessage(tx.QueryRow(ctx, selectMessage+` WHERE provider_id=$1 AND provider_ref=$2`, providerID, providerRef))
		if errors.Is(err, domain.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		m, found = mm, true
		return nil
	})
	return m, found, err
}

func (r *Repo) UpdateDeliveryStatus(ctx context.Context, m domain.Message) error {
	return r.Atomic(ctx, m.OwnerID, func(tx ports.Tx) error {
		return tx.UpdateMessage(ctx, m)
	})
}

// --- templates ---

// GetTemplate resolves the owner's override for templateID, falling back to the
// platform default owner so built-in copy (OTP, invites) exists even before an owner
// configures anything.
func (r *Repo) GetTemplate(ctx context.Context, ownerID, templateID string) (domain.Template, error) {
	t, err := r.getTemplateForOwner(ctx, ownerID, templateID)
	if err == nil {
		return t, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return domain.Template{}, err
	}
	// Fall back to the platform default template.
	return r.getTemplateForOwner(ctx, DefaultTemplateOwner, templateID)
}

func (r *Repo) getTemplateForOwner(ctx context.Context, ownerID, templateID string) (domain.Template, error) {
	var t domain.Template
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var err error
		t, err = scanTemplate(tx.QueryRow(ctx, selectTemplate+` WHERE owner_id=$1 AND id=$2`, ownerID, templateID))
		return err
	})
	return t, err
}

func (r *Repo) ListTemplates(ctx context.Context, ownerID string) ([]domain.Template, error) {
	var out []domain.Template
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectTemplate+` WHERE owner_id=$1 ORDER BY id`, ownerID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			t, err := scanTemplate(rows)
			if err != nil {
				return err
			}
			out = append(out, t)
		}
		return rows.Err()
	})
	return out, err
}

func (r *Repo) UpsertTemplate(ctx context.Context, t domain.Template) error {
	return pg.WithTenant(ctx, r.pool, t.OwnerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO templates (owner_id, id, channel, subject, body, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (owner_id, id) DO UPDATE
			SET channel=EXCLUDED.channel, subject=EXCLUDED.subject, body=EXCLUDED.body, updated_at=EXCLUDED.updated_at`,
			t.OwnerID, t.ID, string(t.Channel), t.Subject, t.Body, t.UpdatedAt)
		return err
	})
}

// MarkEventProcessed records eventID in processed_events; a fresh insert returns
// isNew=true. A conflict (already processed) returns isNew=false. Runs under the
// empty tenant (webhook context).
func (r *Repo) MarkEventProcessed(ctx context.Context, eventID string) (bool, error) {
	var isNew bool
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			INSERT INTO processed_events (event_id, processed_at)
			VALUES ($1, $2) ON CONFLICT (event_id) DO NOTHING`,
			eventID, time.Now().UTC())
		if err != nil {
			return err
		}
		isNew = ct.RowsAffected() == 1
		return nil
	})
	return isNew, err
}

// --- scan helpers ---

const selectMessage = `SELECT id, owner_id, restaurant_id, channel, recipient, template_id, vars,
	subject, body, status, provider_id, provider_ref, idempotency_key, error, created_at, updated_at FROM messages`

const selectTemplate = `SELECT owner_id, id, channel, subject, body, updated_at FROM templates`

type scanner interface {
	Scan(dest ...any) error
}

func scanMessage(row scanner) (domain.Message, error) {
	var (
		m                                     domain.Message
		restaurantID, providerID, providerRef *string
		idemKey, errMsg                       *string
		vars                                  map[string]string
		created, updated                      time.Time
		channel, status                       string
	)
	if err := row.Scan(&m.ID, &m.OwnerID, &restaurantID, &channel, &m.To, &m.TemplateID, &vars,
		&m.Subject, &m.Body, &status, &providerID, &providerRef, &idemKey, &errMsg, &created, &updated); err != nil {
		return domain.Message{}, mapRead(err)
	}
	m.RestaurantID = deref(restaurantID)
	m.Channel = domain.Channel(channel)
	m.Status = domain.DeliveryStatus(status)
	m.ProviderID = deref(providerID)
	m.ProviderRef = deref(providerRef)
	m.IdempotencyKey = deref(idemKey)
	m.Error = deref(errMsg)
	if vars == nil {
		vars = map[string]string{}
	}
	m.Vars = vars
	m.CreatedAt = created
	m.UpdatedAt = updated
	return m, nil
}

func scanTemplate(row scanner) (domain.Template, error) {
	var (
		t       domain.Template
		channel string
		updated time.Time
	)
	if err := row.Scan(&t.OwnerID, &t.ID, &channel, &t.Subject, &t.Body, &updated); err != nil {
		return domain.Template{}, mapRead(err)
	}
	t.Channel = domain.Channel(channel)
	t.UpdatedAt = updated
	return t, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
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
