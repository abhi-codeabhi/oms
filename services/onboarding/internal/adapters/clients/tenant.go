package clients

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	tenantv1 "github.com/restorna/platform/gen/go/restorna/tenant/v1"
	"github.com/restorna/platform/gen/go/restorna/tenant/v1/tenantv1connect"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Tenant wraps the generated TenantService client.
type Tenant struct {
	rpc tenantv1connect.TenantServiceClient
}

var _ ports.Tenant = (*Tenant)(nil)

// NewTenant dials the tenant service at baseURL using the given (h2c) client.
func NewTenant(httpClient connect.HTTPClient, baseURL string) *Tenant {
	return &Tenant{rpc: tenantv1connect.NewTenantServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewTenantFromRPC wraps an existing generated client (tests / custom wiring).
func NewTenantFromRPC(rpc tenantv1connect.TenantServiceClient) *Tenant {
	return &Tenant{rpc: rpc}
}

// CreateOwner provisions the owner record.
func (c *Tenant) CreateOwner(ctx context.Context, name, legalName, country string) (string, error) {
	res, err := c.rpc.CreateOwner(ctx, connect.NewRequest(&tenantv1.CreateOwnerRequest{
		Name:      name,
		LegalName: legalName,
		Country:   country,
	}))
	if err != nil {
		return "", fmt.Errorf("tenant.CreateOwner: %w", err)
	}
	return res.Msg.GetOwner().GetId(), nil
}

// CreateBrand provisions the first brand under the owner.
func (c *Tenant) CreateBrand(ctx context.Context, ownerID, name, primaryColor string) (string, error) {
	res, err := c.rpc.CreateBrand(ctx, connect.NewRequest(&tenantv1.CreateBrandRequest{
		OwnerId:      ownerID,
		Name:         name,
		PrimaryColor: primaryColor,
	}))
	if err != nil {
		return "", fmt.Errorf("tenant.CreateBrand: %w", err)
	}
	return res.Msg.GetBrand().GetId(), nil
}

// SetBrandLogo uploads the brand logo bytes (as an Asset) and returns its URL.
func (c *Tenant) SetBrandLogo(ctx context.Context, brandID string, logo []byte, contentType string) (string, error) {
	res, err := c.rpc.SetBrandLogo(ctx, connect.NewRequest(&tenantv1.SetBrandLogoRequest{
		BrandId: brandID,
		Logo: &commonv1.Asset{
			// The id is assigned by the tenant service on store; we pass the raw
			// bytes via the url field as a data URI fallback when no object store
			// is wired. The tenant service is responsible for persisting the asset.
			ContentType: contentType,
			Url:         dataURI(contentType, logo),
		},
	}))
	if err != nil {
		return "", fmt.Errorf("tenant.SetBrandLogo: %w", err)
	}
	return res.Msg.GetBrand().GetLogo().GetUrl(), nil
}

// CreateRestaurant provisions the first outlet under the brand.
func (c *Tenant) CreateRestaurant(ctx context.Context, brandID, name, address, timezone, gstin string) (string, error) {
	res, err := c.rpc.CreateRestaurant(ctx, connect.NewRequest(&tenantv1.CreateRestaurantRequest{
		BrandId:  brandID,
		Name:     name,
		Address:  address,
		Timezone: timezone,
		Gstin:    gstin,
	}))
	if err != nil {
		return "", fmt.Errorf("tenant.CreateRestaurant: %w", err)
	}
	return res.Msg.GetRestaurant().GetId(), nil
}

// dataURI encodes small logo bytes as a data: URI so the upload travels with the
// Asset when no object-store adapter is configured. The tenant service may
// re-store it; onboarding only needs the bytes delivered once.
func dataURI(contentType string, b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	return "data:" + contentType + ";base64," + base64Encode(b)
}
