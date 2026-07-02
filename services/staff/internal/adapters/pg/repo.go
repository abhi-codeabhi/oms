// Package pg implements the staff Repo over Postgres via pgx. It is RLS-aware:
// every operation runs inside pkg/pg.WithTenant, which sets app.tenant_id on the
// transaction so the row-level security policy scopes rows to the owner. Events
// are staged in the SAME transaction via the transactional outbox.
package pg

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	pgx5 "github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/staff/internal/domain"
	"github.com/restorna/platform/services/staff/internal/ports"
)

// Repo is the Postgres-backed staff roster repository.
type Repo struct {
	pool *pgxpool.Pool
}

var _ ports.Repo = (*Repo)(nil)

// New builds the repo over a pgx pool.
func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// Create inserts a member and stages publish (if any) in one transaction.
func (r *Repo) Create(ctx context.Context, tenantID string, m domain.Member, publish *ports.OutboxEvent) error {
	return pgx5.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO staff_members
			  (id, owner_id, brand_id, restaurant_id, name, email, phone, role, active, user_id, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
			m.ID, m.OwnerID, m.BrandID, m.RestaurantID, m.Name, m.Email, m.Phone,
			int32(m.Role), m.Active, nullable(m.UserID), m.CreatedAt, m.UpdatedAt,
		)
		if err != nil {
			return err
		}
		return stage(tx, tenantID, publish)
	})
}

// Update persists changes to an existing member and stages publish (if any).
func (r *Repo) Update(ctx context.Context, tenantID string, m domain.Member, publish *ports.OutboxEvent) error {
	return pgx5.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `
			UPDATE staff_members
			   SET name=$2, email=$3, phone=$4, role=$5, active=$6, user_id=$7, updated_at=$8
			 WHERE id=$1`,
			m.ID, m.Name, m.Email, m.Phone, int32(m.Role), m.Active, nullable(m.UserID), m.UpdatedAt,
		)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return domain.ErrStaffNotFound
		}
		return stage(tx, tenantID, publish)
	})
}

// Get loads a single member by id (RLS scopes it to the tenant).
func (r *Repo) Get(ctx context.Context, tenantID, staffID string) (domain.Member, error) {
	var m domain.Member
	err := pgx5.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, owner_id, brand_id, restaurant_id, name, email, phone, role, active,
			       COALESCE(user_id,''), created_at, updated_at
			  FROM staff_members WHERE id=$1`, staffID)
		return scan(row, &m)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Member{}, domain.ErrStaffNotFound
	}
	return m, err
}

// ListByRestaurant returns a page of members for an outlet plus the total count.
func (r *Repo) ListByRestaurant(ctx context.Context, tenantID, restaurantID string, limit, offset int) ([]domain.Member, int, error) {
	var members []domain.Member
	var total int
	err := pgx5.WithTenant(ctx, r.pool, tenantID, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx,
			`SELECT count(*) FROM staff_members WHERE restaurant_id=$1`, restaurantID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT id, owner_id, brand_id, restaurant_id, name, email, phone, role, active,
			       COALESCE(user_id,''), created_at, updated_at
			  FROM staff_members
			 WHERE restaurant_id=$1
			 ORDER BY id
			 LIMIT $2 OFFSET $3`, restaurantID, limit, offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var m domain.Member
			if err := scan(rows, &m); err != nil {
				return err
			}
			members = append(members, m)
		}
		return rows.Err()
	})
	return members, total, err
}

// scanner abstracts pgx.Row / pgx.Rows for the shared scan helper.
type scanner interface {
	Scan(dest ...any) error
}

func scan(s scanner, m *domain.Member) error {
	var role int32
	if err := s.Scan(&m.ID, &m.OwnerID, &m.BrandID, &m.RestaurantID, &m.Name, &m.Email,
		&m.Phone, &role, &m.Active, &m.UserID, &m.CreatedAt, &m.UpdatedAt); err != nil {
		return err
	}
	m.Role = roleFromInt(role)
	return nil
}

func stage(tx pgx.Tx, tenantID string, publish *ports.OutboxEvent) error {
	if publish == nil {
		return nil
	}
	e := events.New(publish.Type, tenantID, publish.Data)
	return outbox.Stage(tx, e)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}
