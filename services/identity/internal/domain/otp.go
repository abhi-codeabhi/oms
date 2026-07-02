package domain

import (
	"crypto/subtle"
	"time"

	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
)

// OTP policy constants. These are domain rules, not config, so they live here
// and are exercised by unit tests.
const (
	// OtpTTL is how long a challenge stays valid after creation.
	OtpTTL = 5 * time.Minute
	// MaxOtpAttempts caps wrong guesses before the challenge is locked.
	MaxOtpAttempts = 5
	// DevCode always verifies when the service runs with APP_ENV=dev.
	DevCode = "123456"
)

// OtpChallenge is a pending verification: a hashed/plain code bound to an
// address+realm with a TTL and an attempt counter. The code itself is never
// returned to the client; only the challenge ID is.
type OtpChallenge struct {
	ID        string
	Channel   Channel
	Address   string
	Realm     Realm
	Code      string // the expected code (delivered out of band by the Sender)
	Attempts  int
	ExpiresAt time.Time
	CreatedAt time.Time
	Consumed  bool
}

// NewChallenge builds a challenge that expires OtpTTL from now. ID and code are
// supplied by the app layer (ids + random code) to keep the domain pure.
func NewChallenge(id, code, address string, channel Channel, realm Realm, now time.Time) OtpChallenge {
	return OtpChallenge{
		ID:        id,
		Channel:   channel,
		Address:   address,
		Realm:     realm,
		Code:      code,
		ExpiresAt: now.Add(OtpTTL),
		CreatedAt: now,
	}
}

// expired reports whether the challenge has passed its TTL.
func (c OtpChallenge) expired(now time.Time) bool { return now.After(c.ExpiresAt) }

// Verify checks a candidate code against the challenge at time now. devMode
// makes the well-known DevCode always pass (for local/dev environments only).
//
// On a wrong but non-fatal guess it returns the MUTATED challenge (Attempts+1)
// alongside ErrCodeMismatch / ErrTooManyAttempts so the caller can persist the
// new attempt count. On success it returns the challenge marked Consumed.
func (c OtpChallenge) Verify(code string, now time.Time, devMode bool) (OtpChallenge, error) {
	if c.Consumed {
		return c, ErrChallengeConsumed
	}
	if c.expired(now) {
		return c, ErrChallengeExpired
	}
	if c.Attempts >= MaxOtpAttempts {
		return c, ErrTooManyAttempts
	}

	ok := constantEq(code, c.Code)
	if devMode && code == DevCode {
		ok = true
	}
	if !ok {
		c.Attempts++
		if c.Attempts >= MaxOtpAttempts {
			return c, ErrTooManyAttempts
		}
		return c, ErrCodeMismatch
	}

	c.Consumed = true
	return c, nil
}

// constantEq is a length-safe constant-time string compare.
func constantEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ValidateStartOtp checks an inbound StartOtp request shape before a challenge
// is created.
func ValidateStartOtp(channel Channel, address string) error {
	if address == "" {
		return ErrInvalidAddress
	}
	switch channel {
	case identityv1.Channel_CHANNEL_EMAIL, identityv1.Channel_CHANNEL_PHONE:
		return nil
	default:
		return ErrInvalidChannel
	}
}
