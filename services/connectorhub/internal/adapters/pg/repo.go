// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the owner. Secret config is stored in an encrypted BYTEA column; the repo
// never decrypts (that is the app's Crypto port). Outbox events are staged in the
// same tx (pkg/outbox.Stage).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

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
		return fn(&txAdapter{tx: tx, ownerID: ownerID})
	})
}

// txAdapter implements ports.Tx over a single pgx.Tx.
type txAdapter struct {
	tx      pgx.Tx
	ownerID string
}

func (t *txAdapter) InsertInstallation(ctx context.Context, i domain.Installation) error {
	pub, err := json.Marshal(i.PublicConfig)
	if err != nil {
		return err
	}
	_, err = t.tx.Exec(ctx, `
		INSERT INTO installations
			(id, owner_id, restaurant_id, connector_id, enabled, test_mode, public_config, secret_config, installed_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		i.ID, i.OwnerID, nullStr(i.RestaurantID), i.ConnectorID, i.Enabled, i.TestMode,
		pub, i.SecretConfig, i.InstalledAt, i.UpdatedAt)
	return mapWrite(err)
}

func (t *txAdapter) UpdateInstallation(ctx context.Context, i domain.Installation) error {
	pub, err := json.Marshal(i.PublicConfig)
	if err != nil {
		return err
	}
	ct, err := t.tx.Exec(ctx, `
		UPDATE installations
		SET enabled=$2, test_mode=$3, public_config=$4, secret_config=$5, updated_at=$6
		WHERE id=$1`,
		i.ID, i.Enabled, i.TestMode, pub, i.SecretConfig, i.UpdatedAt)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) GetInstallation(ctx context.Context, installationID string) (domain.Installation, error) {
	return scanInstallation(t.tx.QueryRow(ctx, selectInstallation+` WHERE id=$1`, installationID))
}

func (t *txAdapter) ExistsForConnector(ctx context.Context, ownerID, restaurantID, connectorID string) (bool, error) {
	var n int
	err := t.tx.QueryRow(ctx, `
		SELECT count(*) FROM installations
		WHERE owner_id=$1 AND connector_id=$2 AND coalesce(restaurant_id,'')=$3`,
		ownerID, connectorID, restaurantID).Scan(&n)
	if err != nil {
		return false, mapRead(err)
	}
	return n > 0, nil
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, ownerID string, data any) error {
	e := events.New(eventType, ownerID, data)
	return outbox.Stage(t.tx, e)
}

// --- read methods (each opens its own tenant-scoped tx) ---

func (r *Repo) GetInstallation(ctx context.Context, ownerID, installationID string) (domain.Installation, error) {
	var out domain.Installation
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var err error
		out, err = scanInstallation(tx.QueryRow(ctx, selectInstallation+` WHERE id=$1`, installationID))
		return err
	})
	return out, err
}

func (r *Repo) ListInstallations(ctx context.Context, ownerID string) ([]domain.Installation, error) {
	return r.ListByOwner(ctx, ownerID)
}

func (r *Repo) ListByOwner(ctx context.Context, ownerID string) ([]domain.Installation, error) {
	var list []domain.Installation
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, selectInstallation+` WHERE owner_id=$1 ORDER BY installed_at DESC, id DESC`, ownerID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			inst, err := scanInstallation(rows)
			if err != nil {
				return err
			}
			list = append(list, inst)
		}
		return mapRead(rows.Err())
	})
	return list, err
}

// --- scan helpers ---

const selectInstallation = `SELECT id, owner_id, restaurant_id, connector_id, enabled, test_mode, public_config, secret_config, installed_at, updated_at FROM installations`

type scanner interface {
	Scan(dest ...any) error
}

func scanInstallation(row scanner) (domain.Installation, error) {
	var (
		i         domain.Installation
		rest      *string
		pubRaw    []byte
		secret    []byte
		installed time.Time
		updated   time.Time
	)
	if err := row.Scan(&i.ID, &i.OwnerID, &rest, &i.ConnectorID, &i.Enabled, &i.TestMode, &pubRaw, &secret, &installed, &updated); err != nil {
		return domain.Installation{}, mapRead(err)
	}
	if rest != nil {
		i.RestaurantID = *rest
	}
	i.PublicConfig = map[string]string{}
	if len(pubRaw) > 0 {
		if err := json.Unmarshal(pubRaw, &i.PublicConfig); err != nil {
			return domain.Installation{}, err
		}
	}
	i.SecretConfig = secret
	i.InstalledAt = installed
	i.UpdatedAt = updated
	return i, nil
}

func nullStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
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
		return fmt.Errorf("%w: duplicate", domain.ErrAlreadyExists)
	}
	return err
}
