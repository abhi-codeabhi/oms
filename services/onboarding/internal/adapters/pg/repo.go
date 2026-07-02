// Package pg implements the onboarding Repo over Postgres via pgx. It is
// RLS-aware: every operation runs inside pkg/pg.WithTenant, which sets
// app.tenant_id on the transaction so the row-level security policy scopes rows
// to the owner. Events are staged in the SAME transaction via the outbox.
//
// Until the ACCOUNT step assigns the owner id, the saga row carries the owner id
// produced by tenant.CreateOwner (set before the first persist), so RLS keys on
// it from creation onward.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	pgx5 "github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/onboarding/internal/domain"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Repo is the Postgres-backed onboarding saga repository.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repo = (*Repo)(nil)

// New builds the repo over a pgx pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Create inserts a new saga row and stages publish (if any) in one transaction.
func (r *Repo) Create(ctx context.Context, s domain.State, publish *ports.OutboxEvent) error {
	return pgx5.WithTenant(ctx, r.pool, s.OwnerID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO onboarding_states
			  (id, owner_id, user_id, brand_id, logo_url, outlet_id, plan_id, completed, done, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			s.ID, s.OwnerID, s.UserID, s.BrandID, s.LogoURL, s.OutletID, s.PlanID,
			encodeSteps(s.Completed()), s.Done, s.CreatedAt, s.UpdatedAt,
		)
		if err != nil {
			return err
		}
		return stage(tx, s.OwnerID, publish)
	})
}

// Save upserts the saga row after a step advances and stages publish (if any).
func (r *Repo) Save(ctx context.Context, s domain.State, publish *ports.OutboxEvent) error {
	return pgx5.WithTenant(ctx, r.pool, s.OwnerID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE onboarding_states
			   SET user_id=$2, brand_id=$3, logo_url=$4, outlet_id=$5, plan_id=$6,
			       completed=$7, done=$8, updated_at=$9
			 WHERE id=$1`,
			s.ID, s.UserID, s.BrandID, s.LogoURL, s.OutletID, s.PlanID,
			encodeSteps(s.Completed()), s.Done, s.UpdatedAt,
		)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrNotFound
		}
		return stage(tx, s.OwnerID, publish)
	})
}

// Get loads a saga by its onb_ primary key. The saga owner is not necessarily in
// the caller's scope (a platform admin onboards a not-yet-scoped owner), so the
// lookup runs under an empty tenant: the RLS policy permits the unset-tenant
// control-plane path and the unique primary key bounds the read. App-layer scope
// enforcement (load()) then asserts ownership for owner callers.
func (r *Repo) Get(ctx context.Context, onboardingID string) (domain.State, error) {
	var s domain.State
	err := pgx5.WithTenant(ctx, r.pool, "", func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, owner_id, user_id, brand_id, logo_url, outlet_id, plan_id,
			       completed, done, created_at, updated_at
			  FROM onboarding_states WHERE id=$1`, onboardingID)
		var steps []int32
		var id, ownerCol, userCol, brandCol, logoCol, outletCol, planCol string
		var done bool
		var createdAt, updatedAt = s.CreatedAt, s.UpdatedAt
		if err := row.Scan(&id, &ownerCol, &userCol, &brandCol, &logoCol, &outletCol, &planCol,
			&steps, &done, &createdAt, &updatedAt); err != nil {
			return err
		}
		s = domain.Rebuild(id, ownerCol, userCol, brandCol, logoCol, outletCol, planCol,
			decodeSteps(steps), done, createdAt, updatedAt)
		return nil
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.State{}, domain.ErrNotFound
	}
	return s, err
}

func stage(tx pgx.Tx, tenantID string, publish *ports.OutboxEvent) error {
	if publish == nil {
		return nil
	}
	e := events.New(publish.Type, tenantID, publish.Data)
	return outbox.Stage(tx, e)
}

// encodeSteps serialises completed steps to int4[] for the column.
func encodeSteps(steps []onboardingv1.Step) []int32 {
	out := make([]int32, len(steps))
	for i, s := range steps {
		out[i] = int32(s)
	}
	return out
}

// decodeSteps maps the stored int4[] back to Step values.
func decodeSteps(raw []int32) []onboardingv1.Step {
	out := make([]onboardingv1.Step, len(raw))
	for i, v := range raw {
		out[i] = onboardingv1.Step(v)
	}
	return out
}
