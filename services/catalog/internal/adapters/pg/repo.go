// Package pg is the Postgres implementation of ports.Repository using pgx. Every
// operation runs inside pkg/pg.WithTenant so app.tenant_id is set and RLS scopes
// rows to the outlet (restaurant_id). Outbox events are staged in the same tx
// (pkg/outbox.Stage). Brand items live in `items`; per-outlet price/availability
// overrides live in `item_overrides`; ListItems applies the override.
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
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/catalog/internal/domain"
	"github.com/restorna/platform/services/catalog/internal/ports"
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

func (t *txAdapter) UpsertCategory(ctx context.Context, c domain.Category) error {
	_, err := t.tx.Exec(ctx, `
		INSERT INTO categories (id, restaurant_id, name, sort)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, sort = EXCLUDED.sort`,
		c.ID, t.restaurantID, c.Name, c.Sort)
	return mapWrite(err)
}

func (t *txAdapter) UpsertItem(ctx context.Context, it domain.Item) error {
	tags, err := json.Marshal(it.Tags)
	if err != nil {
		return err
	}
	imgID, imgURL, imgCT := assetCols(it.Image)
	_, err = t.tx.Exec(ctx, `
		INSERT INTO items
			(id, restaurant_id, category_id, name, description, price_minor, currency,
			 veg, tags, prep_minutes, station, available, image_id, image_url, image_content_type,
			 created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (id) DO UPDATE SET
			category_id = EXCLUDED.category_id,
			name = EXCLUDED.name,
			description = EXCLUDED.description,
			price_minor = EXCLUDED.price_minor,
			currency = EXCLUDED.currency,
			veg = EXCLUDED.veg,
			tags = EXCLUDED.tags,
			prep_minutes = EXCLUDED.prep_minutes,
			station = EXCLUDED.station,
			available = EXCLUDED.available,
			image_id = EXCLUDED.image_id,
			image_url = EXCLUDED.image_url,
			image_content_type = EXCLUDED.image_content_type,
			updated_at = EXCLUDED.updated_at`,
		it.ID, t.restaurantID, it.CategoryID, it.Name, it.Description,
		it.Price.Minor, it.Price.Currency, it.Veg, tags, it.PrepMinutes, it.Station,
		it.Available, imgID, imgURL, imgCT, it.CreatedAt, it.UpdatedAt)
	return mapWrite(err)
}

func (t *txAdapter) GetItem(ctx context.Context, itemID string) (domain.Item, error) {
	it, err := scanItem(t.tx.QueryRow(ctx, selectItem+` WHERE id=$1`, itemID))
	if err != nil {
		return domain.Item{}, err
	}
	ov, ok, err := t.GetOverride(ctx, itemID)
	if err != nil {
		return domain.Item{}, err
	}
	if ok {
		return it.Effective(&ov), nil
	}
	return it, nil
}

func (t *txAdapter) GetOverride(ctx context.Context, itemID string) (domain.OutletOverride, bool, error) {
	var (
		ov          domain.OutletOverride
		priceMinor  *int64
		currency    *string
		hasAvail    bool
		available   bool
	)
	row := t.tx.QueryRow(ctx, `
		SELECT item_id, restaurant_id, price_minor, currency, available, has_avail
		FROM item_overrides WHERE item_id=$1 AND restaurant_id=$2`,
		itemID, t.restaurantID)
	if err := row.Scan(&ov.ItemID, &ov.RestaurantID, &priceMinor, &currency, &available, &hasAvail); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.OutletOverride{}, false, nil
		}
		return domain.OutletOverride{}, false, err
	}
	if priceMinor != nil {
		ccy := "INR"
		if currency != nil {
			ccy = *currency
		}
		m := money.New(*priceMinor, ccy)
		ov.Price = &m
	}
	ov.Available = available
	ov.HasAvail = hasAvail
	return ov, true, nil
}

func (t *txAdapter) PutOverride(ctx context.Context, ov domain.OutletOverride) error {
	var priceMinor *int64
	var currency *string
	if ov.Price != nil {
		priceMinor = &ov.Price.Minor
		currency = &ov.Price.Currency
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO item_overrides (item_id, restaurant_id, price_minor, currency, available, has_avail)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (item_id, restaurant_id) DO UPDATE SET
			price_minor = EXCLUDED.price_minor,
			currency = EXCLUDED.currency,
			available = EXCLUDED.available,
			has_avail = EXCLUDED.has_avail`,
		ov.ItemID, t.restaurantID, priceMinor, currency, ov.Available, ov.HasAvail)
	return mapWrite(err)
}

func (t *txAdapter) ClearOverride(ctx context.Context, itemID string) error {
	_, err := t.tx.Exec(ctx, `DELETE FROM item_overrides WHERE item_id=$1 AND restaurant_id=$2`,
		itemID, t.restaurantID)
	return mapWrite(err)
}

// StageEvent writes a CloudEvents row to the outbox in this same tx.
func (t *txAdapter) StageEvent(ctx context.Context, eventType, tenantID string, data any) error {
	e := events.New(eventType, tenantID, data)
	return outbox.Stage(t.tx, e)
}

// --- read methods (each opens its own tenant-scoped tx) ---

func (r *Repo) ListCategories(ctx context.Context, restaurantID string) ([]domain.Category, error) {
	var cats []domain.Category
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id, name, sort FROM categories WHERE restaurant_id=$1 ORDER BY sort, name`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			var c domain.Category
			if err := rows.Scan(&c.ID, &c.Name, &c.Sort); err != nil {
				return mapRead(err)
			}
			cats = append(cats, c)
		}
		return mapRead(rows.Err())
	})
	return cats, err
}

func (r *Repo) GetItem(ctx context.Context, restaurantID, itemID string) (domain.Item, error) {
	var out domain.Item
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		it, err := scanItem(tx.QueryRow(ctx, selectItem+` WHERE id=$1`, itemID))
		if err != nil {
			return err
		}
		ov, ok, err := scanOverride(ctx, tx, itemID, restaurantID)
		if err != nil {
			return err
		}
		if ok {
			out = it.Effective(&ov)
		} else {
			out = it
		}
		return nil
	})
	return out, err
}

// ListItems returns all brand items with per-outlet overrides applied via a LEFT
// JOIN so a single query yields the effective item.
func (r *Repo) ListItems(ctx context.Context, restaurantID string) ([]domain.Item, error) {
	var items []domain.Item
	err := pg.WithTenant(ctx, r.pool, restaurantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT i.id, i.category_id, i.name, i.description, i.price_minor, i.currency,
			       i.veg, i.tags, i.prep_minutes, i.station, i.available,
			       i.image_id, i.image_url, i.image_content_type,
			       o.price_minor, o.currency, o.available, o.has_avail
			FROM items i
			LEFT JOIN item_overrides o ON o.item_id = i.id AND o.restaurant_id = $1
			WHERE i.restaurant_id = $1
			ORDER BY i.category_id, i.name`, restaurantID)
		if err != nil {
			return mapRead(err)
		}
		defer rows.Close()
		for rows.Next() {
			it, err := scanJoinedItem(rows)
			if err != nil {
				return err
			}
			items = append(items, it)
		}
		return mapRead(rows.Err())
	})
	return items, err
}

// --- scan helpers ---

const selectItem = `
	SELECT id, category_id, name, description, price_minor, currency, veg, tags,
	       prep_minutes, station, available, image_id, image_url, image_content_type,
	       created_at, updated_at
	FROM items`

type scanner interface {
	Scan(dest ...any) error
}

func scanItem(row scanner) (domain.Item, error) {
	var (
		it                      domain.Item
		minorPrice              int64
		currency                string
		tagsRaw                 []byte
		imgID, imgURL, imgCT    *string
		created, updated        time.Time
	)
	if err := row.Scan(&it.ID, &it.CategoryID, &it.Name, &it.Description, &minorPrice, &currency,
		&it.Veg, &tagsRaw, &it.PrepMinutes, &it.Station, &it.Available,
		&imgID, &imgURL, &imgCT, &created, &updated); err != nil {
		return domain.Item{}, mapRead(err)
	}
	it.Price = money.New(minorPrice, currency)
	it.Tags = unmarshalTags(tagsRaw)
	it.Image = asset(imgID, imgURL, imgCT)
	it.CreatedAt = created
	it.UpdatedAt = updated
	return it, nil
}

// scanJoinedItem scans an items row LEFT JOINed with its override and applies it.
func scanJoinedItem(row scanner) (domain.Item, error) {
	var (
		it                      domain.Item
		minorPrice              int64
		currency                string
		tagsRaw                 []byte
		imgID, imgURL, imgCT    *string
		ovMinor                 *int64
		ovCurrency              *string
		ovAvailable             *bool
		ovHasAvail              *bool
	)
	if err := row.Scan(&it.ID, &it.CategoryID, &it.Name, &it.Description, &minorPrice, &currency,
		&it.Veg, &tagsRaw, &it.PrepMinutes, &it.Station, &it.Available,
		&imgID, &imgURL, &imgCT,
		&ovMinor, &ovCurrency, &ovAvailable, &ovHasAvail); err != nil {
		return domain.Item{}, mapRead(err)
	}
	it.Price = money.New(minorPrice, currency)
	it.Tags = unmarshalTags(tagsRaw)
	it.Image = asset(imgID, imgURL, imgCT)

	// Apply the override (if the LEFT JOIN matched).
	if ovHasAvail != nil || ovMinor != nil {
		ov := domain.OutletOverride{ItemID: it.ID}
		if ovMinor != nil {
			ccy := "INR"
			if ovCurrency != nil {
				ccy = *ovCurrency
			}
			m := money.New(*ovMinor, ccy)
			ov.Price = &m
		}
		if ovHasAvail != nil && *ovHasAvail {
			ov.HasAvail = true
			if ovAvailable != nil {
				ov.Available = *ovAvailable
			}
		}
		return it.Effective(&ov), nil
	}
	return it, nil
}

func scanOverride(ctx context.Context, tx pgx.Tx, itemID, restaurantID string) (domain.OutletOverride, bool, error) {
	var (
		ov         domain.OutletOverride
		priceMinor *int64
		currency   *string
		available  bool
		hasAvail   bool
	)
	row := tx.QueryRow(ctx, `
		SELECT item_id, restaurant_id, price_minor, currency, available, has_avail
		FROM item_overrides WHERE item_id=$1 AND restaurant_id=$2`, itemID, restaurantID)
	if err := row.Scan(&ov.ItemID, &ov.RestaurantID, &priceMinor, &currency, &available, &hasAvail); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.OutletOverride{}, false, nil
		}
		return domain.OutletOverride{}, false, mapRead(err)
	}
	if priceMinor != nil {
		ccy := "INR"
		if currency != nil {
			ccy = *currency
		}
		m := money.New(*priceMinor, ccy)
		ov.Price = &m
	}
	ov.Available = available
	ov.HasAvail = hasAvail
	return ov, true, nil
}

func unmarshalTags(raw []byte) map[string]int32 {
	if len(raw) == 0 {
		return map[string]int32{}
	}
	out := map[string]int32{}
	_ = json.Unmarshal(raw, &out)
	return out
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
	if isUniqueViolation(err) {
		return fmt.Errorf("%w: duplicate", errAlreadyExists)
	}
	return err
}
