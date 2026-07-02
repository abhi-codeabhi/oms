package bff

import (
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// --- /api/auth/* -> IdentityService ---
//
// start-otp, verify-otp and customer-session are PUBLIC (no token yet); refresh and
// scoped-token take a refresh/access token in the JSON body. None require the auth
// middleware, so the router mounts these without Require.

// StartOTP POST /api/auth/start-otp -> IdentityService.StartOtp.
func (b *BFF) StartOTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Channel string `json:"channel"` // "email" | "phone"
		Address string `json:"address"`
		Realm   string `json:"realm"` // "platform" | "tenant"
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	resp, err := b.clients.Identity.StartOtp(r.Context(), connect.NewRequest(&identityv1.StartOtpRequest{
		Channel: parseChannel(in.Channel),
		Address: in.Address,
		Realm:   parseRealm(in.Realm),
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"challenge_id": resp.Msg.GetChallengeId()})
}

// VerifyOTP POST /api/auth/verify-otp -> IdentityService.VerifyOtp.
func (b *BFF) VerifyOTP(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ChallengeID string `json:"challenge_id"`
		Code        string `json:"code"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	resp, err := b.clients.Identity.VerifyOtp(r.Context(), connect.NewRequest(&identityv1.VerifyOtpRequest{
		ChallengeId: in.ChallengeID,
		Code:        in.Code,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{
		"tokens": tokenPairJSON(resp.Msg.GetTokens()),
		"user":   userJSON(resp.Msg.GetUser()),
	})
}

// Refresh POST /api/auth/refresh -> IdentityService.Refresh.
func (b *BFF) Refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	resp, err := b.clients.Identity.Refresh(r.Context(), connect.NewRequest(&identityv1.RefreshRequest{
		RefreshToken: in.RefreshToken,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tokens": tokenPairJSON(resp.Msg.GetTokens())})
}

// ScopedToken POST /api/auth/scoped-token -> IdentityService.IssueScopedToken.
// Mints a tenant-scoped session after the user picks an outlet/role. The caller
// must be authenticated; the inbound token is forwarded so identity can authorize.
func (b *BFF) ScopedToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
		Scope  struct {
			OwnerID      string `json:"owner_id"`
			BrandID      string `json:"brand_id"`
			RestaurantID string `json:"restaurant_id"`
		} `json:"scope"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Identity.IssueScopedToken(ctx, connect.NewRequest(&identityv1.IssueScopedTokenRequest{
		UserId: in.UserID,
		Role:   parseRole(in.Role),
		Scope: &commonv1.TenantRef{
			OwnerId:      in.Scope.OwnerID,
			BrandId:      in.Scope.BrandID,
			RestaurantId: in.Scope.RestaurantID,
		},
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tokens": tokenPairJSON(resp.Msg.GetTokens())})
}

// CustomerSession POST /api/auth/customer-session -> IdentityService.CustomerSession.
// PUBLIC: anonymous QR session bound to a restaurant + table.
func (b *BFF) CustomerSession(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RestaurantID string `json:"restaurant_id"`
		Table        string `json:"table"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	resp, err := b.clients.Identity.CustomerSession(r.Context(), connect.NewRequest(&identityv1.CustomerSessionRequest{
		RestaurantId: in.RestaurantID,
		Table:        in.Table,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"tokens": tokenPairJSON(resp.Msg.GetTokens())})
}

// --- JSON projections / enum parsing (shared by auth + /api/me) ---

func tokenPairJSON(t *identityv1.TokenPair) map[string]any {
	if t == nil {
		return nil
	}
	return map[string]any{
		"access_token":  t.GetAccessToken(),
		"refresh_token": t.GetRefreshToken(),
		"expires_in":    t.GetExpiresIn(),
	}
}

func userJSON(u *identityv1.User) map[string]any {
	if u == nil {
		return nil
	}
	return map[string]any{
		"id":           u.GetId(),
		"email":        u.GetEmail(),
		"phone":        u.GetPhone(),
		"display_name": u.GetDisplayName(),
		"realm":        u.GetRealm().String(),
		"active":       u.GetActive(),
	}
}

func parseChannel(s string) identityv1.Channel {
	switch s {
	case "email", "EMAIL":
		return identityv1.Channel_CHANNEL_EMAIL
	case "phone", "PHONE":
		return identityv1.Channel_CHANNEL_PHONE
	default:
		return identityv1.Channel_CHANNEL_UNSPECIFIED
	}
}

func parseRealm(s string) identityv1.Realm {
	switch s {
	case "platform", "PLATFORM":
		return identityv1.Realm_REALM_PLATFORM
	case "tenant", "TENANT":
		return identityv1.Realm_REALM_TENANT
	default:
		return identityv1.Realm_REALM_UNSPECIFIED
	}
}
