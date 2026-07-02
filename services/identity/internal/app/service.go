// Package app holds the identity use cases. It depends only on ports +
// domain. No pgx, no connect, no crypto here — those arrive via ports.
package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"math/big"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/identity/internal/domain"
	"github.com/restorna/platform/services/identity/internal/ports"
)

// Service is the identity use-case layer. Fields are ports so the wiring root
// (main.go) injects concrete adapters and tests inject fakes.
type Service struct {
	users      ports.UserRepo
	challenges ports.ChallengeRepo
	refresh    ports.RefreshRepo
	sender     ports.Sender
	signer     ports.Signer
	clock      ports.Clock
	devMode    bool // APP_ENV=dev: DevCode ("123456") always verifies
}

// Config wires the use-case dependencies.
type Config struct {
	Users      ports.UserRepo
	Challenges ports.ChallengeRepo
	Refresh    ports.RefreshRepo
	Sender     ports.Sender
	Signer     ports.Signer
	Clock      ports.Clock
	DevMode    bool
}

// New builds the Service. A nil clock defaults to the real wall clock.
func New(c Config) *Service {
	if c.Clock == nil {
		c.Clock = ports.RealClock{}
	}
	return &Service{
		users:      c.Users,
		challenges: c.Challenges,
		refresh:    c.Refresh,
		sender:     c.Sender,
		signer:     c.Signer,
		clock:      c.Clock,
		devMode:    c.DevMode,
	}
}

// TokenPair is the access+refresh result of a successful auth.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int64 // seconds
}

// StartOtp validates the request, creates a TTL-bound challenge with a random
// 6-digit code, stores it, and hands the code to the Sender for delivery.
// Returns the challenge id (the code is never returned to the client).
func (s *Service) StartOtp(ctx context.Context, channel domain.Channel, address string, realm domain.Realm) (string, error) {
	if err := domain.ValidateStartOtp(channel, address); err != nil {
		return "", err
	}
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	now := s.clock.Now()
	ch := domain.NewChallenge(ids.New("otp"), code, address, channel, realm, now)
	if err := s.challenges.Save(ctx, ch); err != nil {
		return "", err
	}
	// Delivery is pluggable; the log/no-op sender just records it. Real SMS/
	// email arrives via the notifications service later.
	if err := s.sender.Send(ctx, channel, address, code); err != nil {
		return "", err
	}
	return ch.ID, nil
}

// VerifyOtp checks the code against the stored challenge. On success it
// upserts the user (first login auto-registers), consumes the challenge, and
// issues an unscoped access+refresh token pair.
func (s *Service) VerifyOtp(ctx context.Context, challengeID, code string) (TokenPair, domain.User, error) {
	ch, err := s.challenges.Get(ctx, challengeID)
	if err != nil {
		return TokenPair{}, domain.User{}, err
	}
	now := s.clock.Now()
	updated, verr := ch.Verify(code, now, s.devMode)
	if verr != nil {
		// Persist the mutated attempt count even on a failed guess.
		_ = s.challenges.Update(ctx, updated)
		return TokenPair{}, domain.User{}, verr
	}
	if err := s.challenges.Update(ctx, updated); err != nil {
		return TokenPair{}, domain.User{}, err
	}

	user, err := s.findOrCreateUser(ctx, ch)
	if err != nil {
		return TokenPair{}, domain.User{}, err
	}
	if err := user.EnsureActive(); err != nil {
		return TokenPair{}, domain.User{}, err
	}

	pair, err := s.issue(ctx, domain.UnscopedGrant(user))
	if err != nil {
		return TokenPair{}, domain.User{}, err
	}
	return pair, user, nil
}

// Refresh exchanges a valid refresh token for a new pair, rotating the old one
// (revoke + issue a fresh refresh token preserving role + scope).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (TokenPair, error) {
	rec, err := s.refresh.GetByHash(ctx, hashToken(refreshToken))
	if err != nil {
		return TokenPair{}, err
	}
	now := s.clock.Now()
	if !rec.Usable(now) {
		return TokenPair{}, domain.ErrChallengeExpired
	}
	if err := s.refresh.Revoke(ctx, rec.ID); err != nil {
		return TokenPair{}, err
	}
	return s.issue(ctx, rec.GrantFor())
}

// IssueScopedToken mints a token narrowed to a concrete tenant context + role
// for an existing user (e.g. after the owner picks an outlet).
func (s *Service) IssueScopedToken(ctx context.Context, userID string, scope domain.TenantScope, role commonv1.Role) (TokenPair, error) {
	user, err := s.users.FindByID(ctx, userID)
	if err != nil {
		return TokenPair{}, err
	}
	if err := user.EnsureActive(); err != nil {
		return TokenPair{}, err
	}
	return s.issue(ctx, domain.ScopedGrant(userID, role, scope))
}

// CustomerSession mints an anonymous ROLE_CUSTOMER token bound to one
// restaurant + table (from the QR). No user row is created.
func (s *Service) CustomerSession(ctx context.Context, restaurantID, table string) (TokenPair, error) {
	if restaurantID == "" {
		return TokenPair{}, domain.ErrInvalidAddress
	}
	subject := ids.New("cst")
	return s.issue(ctx, domain.CustomerGrant(subject, restaurantID, table))
}

// IntrospectResult is the verified contents of an access token.
type IntrospectResult struct {
	Active bool
	UserID string
	Role   commonv1.Role
	Scope  domain.TenantScope
}

// Introspect verifies an access token (used by the gateway). An invalid or
// expired token yields {Active:false} rather than an error.
func (s *Service) Introspect(ctx context.Context, accessToken string) (IntrospectResult, error) {
	g, err := s.signer.Verify(accessToken)
	if err != nil {
		return IntrospectResult{Active: false}, nil
	}
	return IntrospectResult{
		Active: true,
		UserID: g.UserID,
		Role:   g.Role,
		Scope:  g.Scope,
	}, nil
}

// issue signs an access token and a rotating refresh token for a grant and
// persists the refresh record (by hash).
func (s *Service) issue(ctx context.Context, g domain.TokenGrant) (TokenPair, error) {
	access, err := s.signer.Sign(g, domain.AccessTTL)
	if err != nil {
		return TokenPair{}, err
	}
	raw, err := randomToken()
	if err != nil {
		return TokenPair{}, err
	}
	now := s.clock.Now()
	rec := domain.NewRefreshToken(ids.New("rft"), g.UserID, hashToken(raw), g, now)
	if err := s.refresh.Save(ctx, rec); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{
		AccessToken:  access,
		RefreshToken: raw,
		ExpiresIn:    int64(domain.AccessTTL.Seconds()),
	}, nil
}

// findOrCreateUser returns the user for the challenge's address+realm, creating
// one on first login (auto-registration).
func (s *Service) findOrCreateUser(ctx context.Context, ch domain.OtpChallenge) (domain.User, error) {
	user, err := s.users.FindByAddress(ctx, ch.Realm, ch.Address)
	if err == nil {
		return user, nil
	}
	if err != domain.ErrUserNotFound {
		return domain.User{}, err
	}
	user = domain.NewUser(ids.New("usr"), ch.Address, ch.Channel, ch.Realm)
	if err := s.users.Create(ctx, user); err != nil {
		return domain.User{}, err
	}
	return user, nil
}

// --- crypto helpers (small, kept here rather than a port) ---

// randomCode returns a 6-digit numeric OTP code.
func randomCode() (string, error) {
	const digits = 6
	max := big.NewInt(1000000)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", err
	}
	s := n.String()
	for len(s) < digits {
		s = "0" + s
	}
	return s, nil
}

// randomToken returns a 256-bit opaque refresh token (hex).
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// hashToken hashes a refresh token for at-rest storage (never store plaintext).
func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
