// Package clients adapts the generated Connect clients of the downstream control-
// plane services to the onboarding ports. These are the outbound ports the saga
// drives: identity, tenant, entitlements, staff, settings. Each wraps one
// generated client and maps the saga's intent to the right RPC(s).
package clients

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/gen/go/restorna/identity/v1/identityv1connect"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Identity wraps the generated IdentityService client.
type Identity struct {
	rpc identityv1connect.IdentityServiceClient
}

var _ ports.Identity = (*Identity)(nil)

// NewIdentity dials the identity service at baseURL using the given (h2c) client.
func NewIdentity(httpClient connect.HTTPClient, baseURL string) *Identity {
	return &Identity{rpc: identityv1connect.NewIdentityServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewIdentityFromRPC wraps an existing generated client (tests / custom wiring).
func NewIdentityFromRPC(rpc identityv1connect.IdentityServiceClient) *Identity {
	return &Identity{rpc: rpc}
}

// EnsureOwnerUser starts an OTP challenge in the TENANT realm for the owner's
// contact, which registers the user if absent. Identity is the source of truth
// for the user record; we derive the channel from whichever contact is present
// and return the challenge id as a correlation token. The owner verifies later
// via the normal OTP flow; here we only need the user to exist so the account is
// linkable. The returned id is the challenge id (used as the user correlation
// until VerifyOtp issues the real user id).
func (c *Identity) EnsureOwnerUser(ctx context.Context, email, phone, _ string) (string, error) {
	channel := commonChannelEmail
	address := email
	if address == "" {
		channel = commonChannelPhone
		address = phone
	}
	res, err := c.rpc.StartOtp(ctx, connect.NewRequest(&identityv1.StartOtpRequest{
		Channel: channel,
		Address: address,
		Realm:   identityv1.Realm_REALM_TENANT,
	}))
	if err != nil {
		return "", fmt.Errorf("identity.StartOtp: %w", err)
	}
	return res.Msg.GetChallengeId(), nil
}

const (
	commonChannelEmail = identityv1.Channel_CHANNEL_EMAIL
	commonChannelPhone = identityv1.Channel_CHANNEL_PHONE
)
