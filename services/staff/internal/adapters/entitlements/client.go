// Package entitlements adapts the generated EntitlementsService Connect client to
// the ports.Entitlements interface the app depends on. This is the outbound port
// to the entitlements control-plane service.
package entitlements

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	"github.com/restorna/platform/services/staff/internal/ports"
)

// Client wraps the generated Connect client.
type Client struct {
	rpc entitlementsv1connect.EntitlementsServiceClient
}

var _ ports.Entitlements = (*Client)(nil)

// New dials the entitlements service at baseURL (e.g. http://entitlements:8080)
// using the given HTTP client (typically an h2c client for plaintext gRPC).
func New(httpClient connect.HTTPClient, baseURL string) *Client {
	return &Client{
		rpc: entitlementsv1connect.NewEntitlementsServiceClient(httpClient, baseURL, connect.WithGRPC()),
	}
}

// NewFromRPC wraps an existing generated client (useful for tests or custom
// wiring).
func NewFromRPC(rpc entitlementsv1connect.EntitlementsServiceClient) *Client {
	return &Client{rpc: rpc}
}

// Reserve calls ReserveQuota and surfaces ok + the upgrade hint. The hint is read
// from a CheckQuota fallback only if the server doesn't echo it; here we keep it
// simple and return the server's `ok`, deriving the hint from a CheckQuota call
// when reservation is rejected.
func (c *Client) Reserve(ctx context.Context, ownerID, key string, delta int64, reservationID string) (bool, string, error) {
	res, err := c.rpc.ReserveQuota(ctx, connect.NewRequest(&entitlementsv1.ReserveQuotaRequest{
		OwnerId:       ownerID,
		Key:           key,
		Delta:         delta,
		ReservationId: reservationID,
	}))
	if err != nil {
		return false, "", fmt.Errorf("entitlements.ReserveQuota: %w", err)
	}
	if res.Msg.GetOk() {
		return true, "", nil
	}
	// Rejected: fetch the upgrade hint via CheckQuota so the owner sees why.
	hint := ""
	if chk, cerr := c.rpc.CheckQuota(ctx, connect.NewRequest(&entitlementsv1.CheckQuotaRequest{
		OwnerId: ownerID,
		Key:     key,
		Delta:   delta,
	})); cerr == nil {
		hint = chk.Msg.GetUpgradeHint()
	}
	return false, hint, nil
}

// Release calls ReleaseQuota; the operation is idempotent by reservation id.
func (c *Client) Release(ctx context.Context, ownerID, key string, delta int64, reservationID string) error {
	_, err := c.rpc.ReleaseQuota(ctx, connect.NewRequest(&entitlementsv1.ReleaseQuotaRequest{
		OwnerId:       ownerID,
		Key:           key,
		Delta:         delta,
		ReservationId: reservationID,
	}))
	if err != nil {
		return fmt.Errorf("entitlements.ReleaseQuota: %w", err)
	}
	return nil
}

// DefaultHTTPClient returns a plain HTTP client suitable for h2c gRPC calls to
// in-cluster services. main.go can pass its own instead.
func DefaultHTTPClient() *http.Client {
	return http.DefaultClient
}
