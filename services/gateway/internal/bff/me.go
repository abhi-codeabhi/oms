package bff

import (
	"net/http"

	"connectrpc.com/connect"

	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// Me GET /api/me -> IdentityService.Introspect on the caller's own token.
// Requires auth; returns the active flag, user id, role, and tenancy scope so the
// console can render the right surface. The verified inbound token is both
// introspected (request body) and forwarded (header).
func (b *BFF) Me(w http.ResponseWriter, r *http.Request) {
	token := fwd(r)
	ctx := clients.WithToken(r.Context(), token)
	resp, err := b.clients.Identity.Introspect(ctx, connect.NewRequest(&identityv1.IntrospectRequest{
		AccessToken: token,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	msg := resp.Msg
	out := map[string]any{
		"active":  msg.GetActive(),
		"user_id": msg.GetUserId(),
		"role":    msg.GetRole().String(),
	}
	if s := msg.GetScope(); s != nil {
		out["scope"] = map[string]string{
			"owner_id":      s.GetOwnerId(),
			"brand_id":      s.GetBrandId(),
			"restaurant_id": s.GetRestaurantId(),
		}
	}
	writeJSON(w, out)
}
