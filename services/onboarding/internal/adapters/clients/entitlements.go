package clients

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Entitlements wraps the generated EntitlementsService client.
type Entitlements struct {
	rpc entitlementsv1connect.EntitlementsServiceClient
}

var _ ports.Entitlements = (*Entitlements)(nil)

// NewEntitlements dials the entitlements service at baseURL using the (h2c) client.
func NewEntitlements(httpClient connect.HTTPClient, baseURL string) *Entitlements {
	return &Entitlements{rpc: entitlementsv1connect.NewEntitlementsServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewEntitlementsFromRPC wraps an existing generated client (tests / custom wiring).
func NewEntitlementsFromRPC(rpc entitlementsv1connect.EntitlementsServiceClient) *Entitlements {
	return &Entitlements{rpc: rpc}
}

// AssignPlan sets the owner's effective plan via SetEntitlement. Re-setting the
// same plan is an idempotent overwrite, so a retried StartOnboarding is safe.
func (c *Entitlements) AssignPlan(ctx context.Context, ownerID, planID string) error {
	_, err := c.rpc.SetEntitlement(ctx, connect.NewRequest(&entitlementsv1.SetEntitlementRequest{
		Entitlement: &entitlementsv1.Entitlement{
			OwnerId: ownerID,
			PlanId:  planID,
		},
	}))
	if err != nil {
		return fmt.Errorf("entitlements.SetEntitlement: %w", err)
	}
	return nil
}
