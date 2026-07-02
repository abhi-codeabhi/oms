// Package pg is the Postgres adapter for the entitlements repositories. It maps
// domain types to rows and implements the transactionally-safe usage accounting
// (SELECT ... FOR UPDATE on usage_counters + a reservations ledger for
// idempotency). Every query runs inside pkg/pg.WithTenant so RLS scopes rows by
// owner_id (app.tenant_id).
package pg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/entitlements/internal/domain"
)

// Repo implements ports.PlanRepo, ports.EntitlementRepo and ports.UsageRepo over
// a pgx pool.
type Repo struct {
	pool *pgxpool.Pool
}

// New constructs a Repo from a connection pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ---- PlanRepo -------------------------------------------------------------

// GetPlan loads a plan by id. Plans are global control-plane data, but we still
// run inside a tenant tx for consistency (the plans table has no RLS restriction
// for SELECT — see migrations). We use the empty tenant since plans aren't
// owner-scoped; pg.WithTenant accepts it and simply sets app.tenant_id to "".
func (r *Repo) GetPlan(ctx context.Context, planID string) (domain.Plan, error) {
	var p domain.Plan
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		var quotas, features []byte
		row := tx.QueryRow(ctx,
			`SELECT id, name, quotas, features FROM plans WHERE id = $1`, planID)
		if err := row.Scan(&p.ID, &p.Name, &quotas, &features); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrPlanNotFound
			}
			return err
		}
		if err := json.Unmarshal(quotas, &p.Quotas); err != nil {
			return fmt.Errorf("decode quotas: %w", err)
		}
		return json.Unmarshal(features, &p.Features)
	})
	return p, err
}

// UpsertPlan inserts or replaces a plan.
func (r *Repo) UpsertPlan(ctx context.Context, p domain.Plan) (domain.Plan, error) {
	quotas, _ := json.Marshal(orEmptyI(p.Quotas))
	features, _ := json.Marshal(orEmptyB(p.Features))
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO plans (id, name, quotas, features)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (id) DO UPDATE
			   SET name = EXCLUDED.name,
			       quotas = EXCLUDED.quotas,
			       features = EXCLUDED.features,
			       updated_at = now()`,
			p.ID, p.Name, quotas, features)
		return err
	})
	return p, err
}

// ListPlans returns every plan in the catalog ordered by id. Plans are global
// control-plane data (no RLS), so we use the empty tenant like GetPlan.
func (r *Repo) ListPlans(ctx context.Context) ([]domain.Plan, error) {
	var plans []domain.Plan
	err := pg.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT id, name, quotas, features FROM plans ORDER BY id`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				p                domain.Plan
				quotas, features []byte
			)
			if err := rows.Scan(&p.ID, &p.Name, &quotas, &features); err != nil {
				return err
			}
			if err := json.Unmarshal(quotas, &p.Quotas); err != nil {
				return fmt.Errorf("decode quotas: %w", err)
			}
			if err := json.Unmarshal(features, &p.Features); err != nil {
				return fmt.Errorf("decode features: %w", err)
			}
			plans = append(plans, p)
		}
		return rows.Err()
	})
	return plans, err
}

// ---- EntitlementRepo ------------------------------------------------------

// GetEntitlement loads the owner's entitlement (RLS-scoped by owner_id).
func (r *Repo) GetEntitlement(ctx context.Context, ownerID string) (domain.Entitlement, error) {
	var e domain.Entitlement
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		var qo, fo []byte
		row := tx.QueryRow(ctx,
			`SELECT owner_id, plan_id, quota_overrides, feature_overrides
			   FROM entitlements WHERE owner_id = $1`, ownerID)
		if err := row.Scan(&e.OwnerID, &e.PlanID, &qo, &fo); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrEntitlementNotFound
			}
			return err
		}
		if err := json.Unmarshal(qo, &e.QuotaOverrides); err != nil {
			return fmt.Errorf("decode quota_overrides: %w", err)
		}
		return json.Unmarshal(fo, &e.FeatureOverrides)
	})
	return e, err
}

// UpsertEntitlement inserts or replaces an owner's entitlement.
func (r *Repo) UpsertEntitlement(ctx context.Context, e domain.Entitlement) (domain.Entitlement, error) {
	qo, _ := json.Marshal(orEmptyI(e.QuotaOverrides))
	fo, _ := json.Marshal(orEmptyB(e.FeatureOverrides))
	err := pg.WithTenant(ctx, r.pool, e.OwnerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO entitlements (owner_id, plan_id, quota_overrides, feature_overrides)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (owner_id) DO UPDATE
			   SET plan_id = EXCLUDED.plan_id,
			       quota_overrides = EXCLUDED.quota_overrides,
			       feature_overrides = EXCLUDED.feature_overrides,
			       updated_at = now()`,
			e.OwnerID, e.PlanID, qo, fo)
		return err
	})
	return e, err
}

// ---- UsageRepo ------------------------------------------------------------

// Used returns the current usage counter for (owner, key); 0 if absent.
func (r *Repo) Used(ctx context.Context, ownerID, key string) (int64, error) {
	var used int64
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx,
			`SELECT used FROM usage_counters WHERE owner_id = $1 AND key = $2`,
			ownerID, key)
		if err := row.Scan(&used); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				used = 0
				return nil
			}
			return err
		}
		return nil
	})
	return used, err
}

// Reserve atomically applies +delta for (owner, key), deduped by reservationID,
// enforcing limit. The whole operation runs in one tx:
//  1. dedupe: if the reservation id already exists, return current remaining
//     without re-applying (idempotent replay).
//  2. lock the counter row (SELECT ... FOR UPDATE, creating it at 0 if missing).
//  3. cap check against limit (unless unlimited / non-positive delta).
//  4. insert the ledger row + increment the counter.
func (r *Repo) Reserve(ctx context.Context, ownerID, key string, delta, limit int64, reservationID string) (int64, error) {
	var remaining int64
	err := pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		// 1. Idempotency: has this reservation already been applied?
		var exists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM reservations WHERE reservation_id = $1)`,
			reservationID).Scan(&exists); err != nil {
			return err
		}

		// 2. Lock (or create) the counter row.
		var used int64
		err := tx.QueryRow(ctx,
			`INSERT INTO usage_counters (owner_id, key, used)
			 VALUES ($1, $2, 0)
			 ON CONFLICT (owner_id, key) DO UPDATE SET used = usage_counters.used
			 RETURNING used`,
			ownerID, key).Scan(&used)
		if err != nil {
			return err
		}
		// Re-read under FOR UPDATE to serialise concurrent reservers.
		if err := tx.QueryRow(ctx,
			`SELECT used FROM usage_counters WHERE owner_id = $1 AND key = $2 FOR UPDATE`,
			ownerID, key).Scan(&used); err != nil {
			return err
		}

		if exists {
			// Already applied — return remaining without re-counting.
			remaining = domain.Remaining(limit, used)
			return nil
		}

		// 3. Cap check.
		if !domain.Allows(limit, used, delta) {
			return domain.ErrQuotaExceeded
		}

		// 4. Record ledger + increment.
		if _, err := tx.Exec(ctx,
			`INSERT INTO reservations (reservation_id, owner_id, key, delta)
			 VALUES ($1, $2, $3, $4)`,
			reservationID, ownerID, key, delta); err != nil {
			return err
		}
		newUsed := used + delta
		if _, err := tx.Exec(ctx,
			`UPDATE usage_counters SET used = $3, updated_at = now()
			   WHERE owner_id = $1 AND key = $2`,
			ownerID, key, newUsed); err != nil {
			return err
		}
		remaining = domain.Remaining(limit, newUsed)
		return nil
	})
	return remaining, err
}

// Release atomically undoes a prior reservation by reservationID. If the ledger
// row is absent (never applied or already released) it's a no-op.
func (r *Repo) Release(ctx context.Context, ownerID, key string, delta int64, reservationID string) error {
	return pg.WithTenant(ctx, r.pool, ownerID, func(tx pgx.Tx) error {
		// Delete the ledger row; the deleted delta is the source of truth so a
		// caller-supplied delta mismatch can't corrupt the counter.
		var applied int64
		err := tx.QueryRow(ctx,
			`DELETE FROM reservations WHERE reservation_id = $1 AND owner_id = $2
			 RETURNING delta`, reservationID, ownerID).Scan(&applied)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil // idempotent no-op
			}
			return err
		}
		// Lock + decrement, clamped at 0.
		var used int64
		if err := tx.QueryRow(ctx,
			`SELECT used FROM usage_counters WHERE owner_id = $1 AND key = $2 FOR UPDATE`,
			ownerID, key).Scan(&used); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		}
		newUsed := used - applied
		if newUsed < 0 {
			newUsed = 0
		}
		_, err = tx.Exec(ctx,
			`UPDATE usage_counters SET used = $3, updated_at = now()
			   WHERE owner_id = $1 AND key = $2`,
			ownerID, key, newUsed)
		return err
	})
}

func orEmptyI(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

func orEmptyB(m map[string]bool) map[string]bool {
	if m == nil {
		return map[string]bool{}
	}
	return m
}
