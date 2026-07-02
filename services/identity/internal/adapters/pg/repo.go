// Package pg implements the identity repositories on Postgres via pgx/v5.
//
// Identity is a CROSS-TENANT control-plane service: a user is not bound to an
// owner/brand/restaurant, so these tables carry NO tenant_id and use no RLS.
// Rows are partitioned logically by `realm` (platform vs tenant) + address.
package pg

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/services/identity/internal/domain"
	"github.com/restorna/platform/services/identity/internal/ports"
)

// Store bundles the three identity repositories over one pgx pool.
type Store struct {
	pool *pgxpool.Pool
}

// New builds the Store.
func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// Users returns the user repository.
func (s *Store) Users() ports.UserRepo { return &userRepo{pool: s.pool} }

// Challenges returns the OTP challenge repository.
func (s *Store) Challenges() ports.ChallengeRepo { return &challengeRepo{pool: s.pool} }

// Refresh returns the refresh-token repository.
func (s *Store) Refresh() ports.RefreshRepo { return &refreshRepo{pool: s.pool} }

// --- users ---

type userRepo struct{ pool *pgxpool.Pool }

func (r *userRepo) FindByAddress(ctx context.Context, realm domain.Realm, address string) (domain.User, error) {
	const q = `
SELECT id, email, phone, display_name, realm, active, created_at
FROM users
WHERE realm = $1 AND (email = $2 OR phone = $2)
LIMIT 1`
	row := r.pool.QueryRow(ctx, q, int32(realm), address)
	return scanUser(row)
}

func (r *userRepo) FindByID(ctx context.Context, id string) (domain.User, error) {
	const q = `
SELECT id, email, phone, display_name, realm, active, created_at
FROM users WHERE id = $1`
	row := r.pool.QueryRow(ctx, q, id)
	return scanUser(row)
}

func (r *userRepo) Create(ctx context.Context, u domain.User) error {
	const q = `
INSERT INTO users (id, email, phone, display_name, realm, active, created_at)
VALUES ($1, NULLIF($2,''), NULLIF($3,''), $4, $5, $6, $7)`
	_, err := r.pool.Exec(ctx, q,
		u.ID, u.Email, u.Phone, u.DisplayName, int32(u.Realm), u.Active, u.CreatedAt)
	return err
}

func scanUser(row pgx.Row) (domain.User, error) {
	var (
		u                  domain.User
		email, phone, name *string
		realm              int32
	)
	err := row.Scan(&u.ID, &email, &phone, &name, &realm, &u.Active, &u.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.User{}, domain.ErrUserNotFound
	}
	if err != nil {
		return domain.User{}, err
	}
	u.Email = deref(email)
	u.Phone = deref(phone)
	u.DisplayName = deref(name)
	u.Realm = identityv1.Realm(realm)
	return u, nil
}

// --- otp challenges ---

type challengeRepo struct{ pool *pgxpool.Pool }

func (r *challengeRepo) Save(ctx context.Context, c domain.OtpChallenge) error {
	const q = `
INSERT INTO otp_challenges
  (id, channel, address, realm, code, attempts, expires_at, created_at, consumed)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	_, err := r.pool.Exec(ctx, q,
		c.ID, int32(c.Channel), c.Address, int32(c.Realm), c.Code,
		c.Attempts, c.ExpiresAt, c.CreatedAt, c.Consumed)
	return err
}

func (r *challengeRepo) Get(ctx context.Context, id string) (domain.OtpChallenge, error) {
	const q = `
SELECT id, channel, address, realm, code, attempts, expires_at, created_at, consumed
FROM otp_challenges WHERE id = $1`
	var (
		c              domain.OtpChallenge
		channel, realm int32
	)
	err := r.pool.QueryRow(ctx, q, id).Scan(
		&c.ID, &channel, &c.Address, &realm, &c.Code,
		&c.Attempts, &c.ExpiresAt, &c.CreatedAt, &c.Consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.OtpChallenge{}, domain.ErrChallengeNotFound
	}
	if err != nil {
		return domain.OtpChallenge{}, err
	}
	c.Channel = identityv1.Channel(channel)
	c.Realm = identityv1.Realm(realm)
	return c, nil
}

func (r *challengeRepo) Update(ctx context.Context, c domain.OtpChallenge) error {
	const q = `
UPDATE otp_challenges SET attempts = $2, consumed = $3 WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, c.ID, c.Attempts, c.Consumed)
	return err
}

// --- refresh tokens ---

type refreshRepo struct{ pool *pgxpool.Pool }

func (r *refreshRepo) Save(ctx context.Context, t domain.RefreshToken) error {
	const q = `
INSERT INTO refresh_tokens
  (id, user_id, token_hash, role, owner_id, brand_id, restaurant_id,
   expires_at, created_at, revoked)
VALUES ($1,$2,$3,$4,NULLIF($5,''),NULLIF($6,''),NULLIF($7,''),$8,$9,$10)`
	_, err := r.pool.Exec(ctx, q,
		t.ID, t.UserID, t.TokenHash, int32(t.Role),
		t.Scope.OwnerID, t.Scope.BrandID, t.Scope.RestaurantID,
		t.ExpiresAt, t.CreatedAt, t.Revoked)
	return err
}

func (r *refreshRepo) GetByHash(ctx context.Context, hash string) (domain.RefreshToken, error) {
	const q = `
SELECT id, user_id, token_hash, role, owner_id, brand_id, restaurant_id,
       expires_at, created_at, revoked
FROM refresh_tokens WHERE token_hash = $1`
	var (
		t                        domain.RefreshToken
		role                     int32
		owner, brand, restaurant *string
	)
	err := r.pool.QueryRow(ctx, q, hash).Scan(
		&t.ID, &t.UserID, &t.TokenHash, &role,
		&owner, &brand, &restaurant,
		&t.ExpiresAt, &t.CreatedAt, &t.Revoked)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.RefreshToken{}, domain.ErrChallengeNotFound
	}
	if err != nil {
		return domain.RefreshToken{}, err
	}
	t.Role = commonv1.Role(role)
	t.Scope = domain.TenantScope{
		OwnerID:      deref(owner),
		BrandID:      deref(brand),
		RestaurantID: deref(restaurant),
	}
	return t, nil
}

func (r *refreshRepo) Revoke(ctx context.Context, id string) error {
	const q = `UPDATE refresh_tokens SET revoked = true WHERE id = $1`
	_, err := r.pool.Exec(ctx, q, id)
	return err
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ensure time is referenced (created_at columns are time.Time).
var _ = time.Time{}

// compile-time port assertions.
var (
	_ ports.UserRepo      = (*userRepo)(nil)
	_ ports.ChallengeRepo = (*challengeRepo)(nil)
	_ ports.RefreshRepo   = (*refreshRepo)(nil)
)
