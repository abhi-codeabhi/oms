// Package pg is the Postgres adapter for the settings repositories. Definitions
// are GLOBAL control-plane data (no RLS); overrides are owner-scoped and every
// override query runs inside pkg/pg.WithTenant so RLS scopes rows by owner_id
// (app.tenant_id). SetOverride stages the change event in the same tx via
// pkg/outbox.Stage so the write and the outbox row commit together.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/settings/internal/domain"
	"github.com/restorna/platform/services/settings/internal/ports"
)

// Repo implements ports.DefinitionRepo and ports.OverrideRepo over a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

var (
	_ ports.DefinitionRepo = (*Repo)(nil)
	_ ports.OverrideRepo   = (*Repo)(nil)
)

// New constructs a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ---- DefinitionRepo (global; no RLS) --------------------------------------

// UpsertDefinitions idempotently inserts/updates definitions by key. We run with
// the empty tenant (definitions aren't owner-scoped); pg.WithTenant sets
// app.tenant_id to "" which the definitions table's (absent) RLS ignores.
func (r *Repo) UpsertDefinitions(ctx context.Context, defs []domain.Definition) (int, error) {
	var count int
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		for _, d := range defs {
			_, err := tx.Exec(ctx, `
				INSERT INTO definitions
				    (key, title, description, type, default_type, default_raw,
				     max_scope, enum_options, validation, editable_by, feature_gated)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
				ON CONFLICT (key) DO UPDATE SET
				    title         = EXCLUDED.title,
				    description   = EXCLUDED.description,
				    type          = EXCLUDED.type,
				    default_type  = EXCLUDED.default_type,
				    default_raw   = EXCLUDED.default_raw,
				    max_scope     = EXCLUDED.max_scope,
				    enum_options  = EXCLUDED.enum_options,
				    validation    = EXCLUDED.validation,
				    editable_by   = EXCLUDED.editable_by,
				    feature_gated = EXCLUDED.feature_gated,
				    updated_at    = now()`,
				d.Key, d.Title, d.Description, int(d.Type),
				int(d.Default.Type), d.Default.Raw,
				int(d.MaxScope), d.EnumOptions, d.Validation, d.EditableBy, d.FeatureGated)
			if err != nil {
				return err
			}
			count++
		}
		return nil
	})
	return count, err
}

// GetDefinition loads one definition by key.
func (r *Repo) GetDefinition(ctx context.Context, key string) (domain.Definition, error) {
	var d domain.Definition
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, selectDefinition+` WHERE key = $1`, key)
		var err error
		d, err = scanDefinition(row)
		return err
	})
	return d, err
}

// ListDefinitions returns the catalog filtered by namespace ("" = all). The
// namespace match is on a dot boundary: key = ns OR key LIKE ns || '.%'.
func (r *Repo) ListDefinitions(ctx context.Context, namespace string) ([]domain.Definition, error) {
	var out []domain.Definition
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		var (
			rows pgx.Rows
			err  error
		)
		if namespace == "" {
			rows, err = tx.Query(ctx, selectDefinition+` ORDER BY key`)
		} else {
			rows, err = tx.Query(ctx,
				selectDefinition+` WHERE key = $1 OR key LIKE $1 || '.%' ORDER BY key`,
				namespace)
		}
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanDefinition(rows)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// ---- OverrideRepo (owner-scoped; RLS) -------------------------------------

// SetOverride upserts an override at its scope and stages the change event in the
// SAME tenant-scoped transaction. The unique key is (key, scope, owner, brand,
// restaurant) — empty-string for the unused tenant columns keeps the conflict
// target total.
func (r *Repo) SetOverride(ctx context.Context, o domain.Override, evt ports.OverrideEvent) error {
	return pg.WithTenant(ctx, r.pool, o.OwnerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO overrides
			    (key, owner_id, brand_id, restaurant_id, scope, value_type, value_raw)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (key, owner_id, brand_id, restaurant_id) DO UPDATE SET
			    scope      = EXCLUDED.scope,
			    value_type = EXCLUDED.value_type,
			    value_raw  = EXCLUDED.value_raw,
			    updated_at = now()`,
			o.Key, o.OwnerID, o.BrandID, o.RestaurantID, int(o.Scope),
			int(o.Value.Type), o.Value.Raw)
		if err != nil {
			return err
		}
		e := events.New(evt.Type, evt.TenantID, evt.Data)
		return outbox.Stage(tx, e)
	})
}

// OverridesFor returns every override that could apply to (owner, brand,
// restaurant) for the given keys: owner-level rows, plus brand-level rows for
// brandID, plus restaurant-level rows for restaurantID. Precedence is resolved in
// the domain. An empty keys slice means "all keys for this owner".
func (r *Repo) OverridesFor(ctx context.Context, ownerID, brandID, restaurantID string, keys []string) ([]domain.Override, error) {
	var out []domain.Override
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		// Build the scope filter: always owner-level; include brand/restaurant rows
		// only for the specific brand/restaurant requested.
		q := selectOverride + `
			WHERE owner_id = $1
			  AND (
			        (scope = $2)
			     OR (scope = $3 AND brand_id = $4)
			     OR (scope = $5 AND restaurant_id = $6)
			      )`
		args := []any{
			ownerID,
			int(domain.ScopeOwner),
			int(domain.ScopeBrand), brandID,
			int(domain.ScopeRestaurant), restaurantID,
		}
		if len(keys) > 0 {
			q += ` AND key = ANY($7)`
			args = append(args, keys)
		}
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			o, err := scanOverride(rows)
			if err != nil {
				return err
			}
			out = append(out, o)
		}
		return rows.Err()
	})
	return out, err
}

// ---- scan helpers ---------------------------------------------------------

const selectDefinition = `
	SELECT key, title, description, type, default_type, default_raw,
	       max_scope, enum_options, validation, editable_by, feature_gated
	FROM definitions`

const selectOverride = `
	SELECT key, owner_id, brand_id, restaurant_id, scope, value_type, value_raw
	FROM overrides`

// scanner abstracts pgx.Row and pgx.Rows for shared scan helpers.
type scanner interface{ Scan(dest ...any) error }

func scanDefinition(row scanner) (domain.Definition, error) {
	var (
		d                                        domain.Definition
		typ, defType, maxScope                   int
		defRaw, title, desc, validation, editBy  string
		enumOpts                                 []string
		featureGated                             bool
	)
	if err := row.Scan(&d.Key, &title, &desc, &typ, &defType, &defRaw,
		&maxScope, &enumOpts, &validation, &editBy, &featureGated); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Definition{}, domain.ErrNotFound
		}
		return domain.Definition{}, err
	}
	d.Title = title
	d.Description = desc
	d.Type = domain.ValueType(typ)
	d.Default = domain.Value{Type: domain.ValueType(defType), Raw: defRaw}
	d.MaxScope = domain.Scope(maxScope)
	d.EnumOptions = enumOpts
	d.Validation = validation
	d.EditableBy = editBy
	d.FeatureGated = featureGated
	return d, nil
}

func scanOverride(row scanner) (domain.Override, error) {
	var (
		o        domain.Override
		scope    int
		valType  int
		valRaw   string
	)
	if err := row.Scan(&o.Key, &o.OwnerID, &o.BrandID, &o.RestaurantID, &scope, &valType, &valRaw); err != nil {
		return domain.Override{}, err
	}
	o.Scope = domain.Scope(scope)
	o.Value = domain.Value{Type: domain.ValueType(valType), Raw: valRaw}
	return o, nil
}
