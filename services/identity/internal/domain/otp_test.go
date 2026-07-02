package domain

import (
	"testing"
	"time"

	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
)

func TestOtpChallenge_Verify(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	base := func() OtpChallenge {
		return NewChallenge("otp_1", "987654", "+15551112222",
			identityv1.Channel_CHANNEL_PHONE, identityv1.Realm_REALM_TENANT, now)
	}

	tests := []struct {
		name        string
		setup       func(OtpChallenge) OtpChallenge
		code        string
		at          time.Time
		devMode     bool
		wantErr     error
		wantConsume bool
		wantAttempt int
	}{
		{
			name:        "correct code verifies and consumes",
			code:        "987654",
			at:          now,
			wantErr:     nil,
			wantConsume: true,
		},
		{
			name:        "wrong code increments attempts",
			code:        "000000",
			at:          now,
			wantErr:     ErrCodeMismatch,
			wantAttempt: 1,
		},
		{
			name:    "expired challenge rejected",
			code:    "987654",
			at:      now.Add(OtpTTL + time.Second),
			wantErr: ErrChallengeExpired,
		},
		{
			name:        "dev code verifies in dev mode",
			code:        DevCode,
			at:          now,
			devMode:     true,
			wantErr:     nil,
			wantConsume: true,
		},
		{
			name:    "dev code rejected outside dev mode",
			code:    DevCode,
			at:      now,
			devMode: false,
			wantErr: ErrCodeMismatch,
		},
		{
			name: "consumed challenge rejected",
			setup: func(c OtpChallenge) OtpChallenge {
				c.Consumed = true
				return c
			},
			code:    "987654",
			at:      now,
			wantErr: ErrChallengeConsumed,
		},
		{
			name: "too many attempts locks",
			setup: func(c OtpChallenge) OtpChallenge {
				c.Attempts = MaxOtpAttempts
				return c
			},
			code:    "987654",
			at:      now,
			wantErr: ErrTooManyAttempts,
		},
		{
			name: "last wrong attempt trips lock",
			setup: func(c OtpChallenge) OtpChallenge {
				c.Attempts = MaxOtpAttempts - 1
				return c
			},
			code:        "000000",
			at:          now,
			wantErr:     ErrTooManyAttempts,
			wantAttempt: MaxOtpAttempts,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			if tc.setup != nil {
				c = tc.setup(c)
			}
			got, err := c.Verify(tc.code, tc.at, tc.devMode)
			if err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if got.Consumed != tc.wantConsume {
				t.Errorf("consumed = %v, want %v", got.Consumed, tc.wantConsume)
			}
			if tc.wantAttempt != 0 && got.Attempts != tc.wantAttempt {
				t.Errorf("attempts = %d, want %d", got.Attempts, tc.wantAttempt)
			}
		})
	}
}

func TestValidateStartOtp(t *testing.T) {
	tests := []struct {
		name    string
		channel Channel
		address string
		wantErr error
	}{
		{"email ok", identityv1.Channel_CHANNEL_EMAIL, "a@b.com", nil},
		{"phone ok", identityv1.Channel_CHANNEL_PHONE, "+15551112222", nil},
		{"empty address", identityv1.Channel_CHANNEL_EMAIL, "", ErrInvalidAddress},
		{"unspecified channel", identityv1.Channel_CHANNEL_UNSPECIFIED, "a@b.com", ErrInvalidChannel},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStartOtp(tc.channel, tc.address); err != tc.wantErr {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
