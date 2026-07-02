// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the owner. Outbox events are staged in the same tx (pkg/outbox.Stage).
package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/tenant/internal/domain"
	"github.com/restorna/platform/services/tenant/internal/ports"
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

func (t *txAdapter) InsertOwner(ctx context.Context, o domain.Owner) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO owners (id, owner_id, name, legal_name, country, created_at)
		VALUES ($1, $1, $2, $3, $4, $5)`,
		o.ID, o.Name, o.LegalName, o.Country, o.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) InsertBrand(ctx context.Context, b domain.Brand) error {
	logoID, logoURL, logoCT := assetCols(b.Logo)
	_, err := t.tx.Exec(ctx, `
		INSERT INTO brands (id, owner_id, name, primary_color, logo_id, logo_url, logo_content_type, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		b.ID, b.OwnerID, b.Name, b.PrimaryColor, logoID, logoURL, logoCT, b.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) InsertRestaurant(ctx context.Context, r domain.Restaurant) error {
	logoID, logoURL, logoCT := assetCols(r.Logo)
	_, err := t.tx.Exec(ctx, `
		INSERT INTO restaurants
			(id, brand_id, owner_id, name, address, timezone, gstin, logo_id, logo_url, logo_content_type, active, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		r.ID, r.BrandID, r.OwnerID, r.Name, r.Address, r.Timezone, r.GSTIN,
		logoID, logoURL, logoCT, r.Active, r.CreatedAt)
	return mapWrite(err)
}

func (t *txAdapter) UpdateBrand(ctx context.Context, b domain.Brand) error {
	logoID, logoURL, logoCT := assetCols(b.Logo)
	ct, err := t.tx.Exec(ctx, `
		UPDATE brands SET name=$2, primary_color=$3, logo_id=$4, logo_url=$5, logo_content_type=$6
		WHERE id=$1`,
		b.ID, b.Name, b.PrimaryColor, logoID, logoURL, logoCT)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) UpdateRestaurant(ctx context.Context, r domain.Restaurant) error {
	logoID, logoURL, logoCT := assetCols(r.Logo)
	ct, err := t.tx.Exec(ctx, `
		UPDATE restaurants
		SET name=$2, address=$3, timezone=$4, gstin=$5, logo_id=$6, logo_url=$7, logo_content_type=$8, active=$9
		WHERE id=$1`,
		r.ID, r.Name, r.Address, r.Timezone, r.GSTIN, logoID, logoURL, logoCT, r.Active)
	if err != nil {
		return mapWrite(err)
	}
	if ct.RowsAffected() == 0 {
		return domain.ErrNotFound
	}
	return nil
}

func (t *txAdapter) GetBrand(ctx context.Context, brandID string) (domain.Brand, error) {
	return scanBrand(t.tx.QueryRow(ctx, selectBrand+` WHERE id=$1`, brandID))
}

func (t *txAdapter) GetRestaurant(ctx context.Context, restaurantID string) (domain.Restaurant, error) {
	return scanRestaurant(t.tx.QueryRow(ctx, selectRestaurant+` WHERE id=$1`, restaurantID))
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, ownerID string, data any) error {
	e := events.New(eventType, ownerID, data)
	return outbox.Stage(t.tx, e)
}

// --- read methods (each opens its own tenant-scoped tx) ---

func (r *Repo) GetOwner(ctx context.Context, ownerID string) (domain.Owner, error) {
	var o domain.Owner
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `SELECT id, name, legal_name, country, created_at FROM owners WHERE id=$1`, ownerID)
		var created time.Time
		if err := row.Scan(&o.ID, &o.Name, &o.LegalName, &o.Country, &created); err != nil {
			return mapRead(err)
		}
		o.CreatedAt = created
		return nil
	})
	return o, err
}

// ListOwners returns a cross-tenant, paginated index of owners for platform
// admins. Unlike the other reads it is NOT owner-scoped: it runs under the empty
// tenant (the admin connection bypasses RLS — see migration notes), so it can see
// every owner. The optional query filters by name (case-insensitive substring).
// The app layer enforces the ROLE_PLATFORM_ADMIN check before calling this.
func (r *Repo) ListOwners(ctx context.Context, query string, limit, offset int) ([]domain.Owner, int, error) {
	var owners []domain.Owner
	var total int
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		like := "%" + query + "%"
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM owners WHERE ($1 = '' OR name ILIKE $2)`,
			query, like).Scan(&total); err != nil {
			return mapRead(err)
		}
		rows, err := tx.Query(ctx,
			`SELECT id, name, legal_name, country, created_at FROM owners
			 WHERE ($1 = '' OR name ILIKE $2)
			 ORDER BY created_at DESC, id DESC LIMIT $3 OFFSET $4`,
			query, like, limit, offset)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			var (
				o       domain.Owner
				created time.Time
			)
			if err := rows.Scan(&o.ID, &o.Name, &o.LegalName, &o.Country, &created); err != nil {
				return mapRead(err)
			}
			o.CreatedAt = created
			owners = append(owners, o)
		}
		return mapRead(rows.Err())
	})
	return owners, total, err
}

func (r *Repo) GetBrand(ctx context.Context, ownerID, brandID string) (domain.Brand, error) {
	var b domain.Brand
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var err error
		b, err = scanBrand(tx.QueryRow(ctx, selectBrand+` WHERE id=$1`, brandID))
		return err
	})
	return b, err
}

func (r *Repo) GetRestaurant(ctx context.Context, ownerID, restaurantID string) (domain.Restaurant, error) {
	var out domain.Restaurant
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var err error
		out, err = scanRestaurant(tx.QueryRow(ctx, selectRestaurant+` WHERE id=$1`, restaurantID))
		return err
	})
	return out, err
}

func (r *Repo) ListBrands(ctx context.Context, ownerID string, limit, offset int) ([]domain.Brand, int, error) {
	var brands []domain.Brand
	var total int
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM brands WHERE owner_id=$1`, ownerID).Scan(&total); err != nil {
			return mapRead(err)
		}
		rows, err := tx.Query(ctx, selectBrand+` WHERE owner_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
			ownerID, limit, offset)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanBrand(rows)
			if err != nil {
				return err
			}
			brands = append(brands, b)
		}
		return mapRead(rows.Err())
	})
	return brands, total, err
}

func (r *Repo) ListRestaurants(ctx context.Context, ownerID, brandID string, limit, offset int) ([]domain.Restaurant, int, error) {
	var list []domain.Restaurant
	var total int
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM restaurants WHERE brand_id=$1`, brandID).Scan(&total); err != nil {
			return mapRead(err)
		}
		rows, err := tx.Query(ctx, selectRestaurant+` WHERE brand_id=$1 ORDER BY created_at DESC, id DESC LIMIT $2 OFFSET $3`,
			brandID, limit, offset)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			rr, err := scanRestaurant(rows)
			if err != nil {
				return err
			}
			list = append(list, rr)
		}
		return mapRead(rows.Err())
	})
	return list, total, err
}

// --- scan helpers ---

const selectBrand = `SELECT id, owner_id, name, primary_color, logo_id, logo_url, logo_content_type, created_at FROM brands`

const selectRestaurant = `SELECT id, brand_id, owner_id, name, address, timezone, gstin, logo_id, logo_url, logo_content_type, active, created_at FROM restaurants`

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface {
	Scan(dest ...any) error
}

func scanBrand(row scanner) (domain.Brand, error) {
	var (
		b                       domain.Brand
		logoID, logoURL, logoCT *string
		created                 time.Time
	)
	if err := row.Scan(&b.ID, &b.OwnerID, &b.Name, &b.PrimaryColor, &logoID, &logoURL, &logoCT, &created); err != nil {
		return domain.Brand{}, mapRead(err)
	}
	b.CreatedAt = created
	b.Logo = asset(logoID, logoURL, logoCT)
	return b, nil
}

func scanRestaurant(row scanner) (domain.Restaurant, error) {
	var (
		r                       domain.Restaurant
		logoID, logoURL, logoCT *string
		created                 time.Time
	)
	if err := row.Scan(&r.ID, &r.BrandID, &r.OwnerID, &r.Name, &r.Address, &r.Timezone, &r.GSTIN,
		&logoID, &logoURL, &logoCT, &r.Active, &created); err != nil {
		return domain.Restaurant{}, mapRead(err)
	}
	r.CreatedAt = created
	r.Logo = asset(logoID, logoURL, logoCT)
	return r, nil
}

func asset(id, url, ct *string) *domain.Asset {
	if url == nil || *url == "" {
		return nil
	}
	a := domain.Asset{URL: *url}
	if id != nil {
		a.ID = *id
	}
	if ct != nil {
		a.ContentType = *ct
	}
	return &a
}

func assetCols(a *domain.Asset) (id, url, ct *string) {
	if a == nil {
		return nil, nil, nil
	}
	return ptr(a.ID), ptr(a.URL), ptr(a.ContentType)
}

func ptr(s string) *string { return &s }

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
	// 23505 = unique_violation -> AlreadyExists at the grpc boundary.
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: duplicate", errAlreadyExists)
	}
	return err
}
