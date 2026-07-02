// Package entitlements adapts the generated EntitlementsService Connect client to
// the app's ports.Entitlements interface. This is the only place that knows about
// the generated client; the app stays infra-free.
package entitlements

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	entitlementsv1 "github.com/restorna/platform/gen/go/restorna/entitlements/v1"
	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// Client implements ports.Entitlements over a Connect EntitlementsService client.
type Client struct {
	svc entitlementsv1connect.EntitlementsServiceClient
}

var _ ports.Entitlements = (*Client)(nil)

// New builds a Client talking to the entitlements service at baseURL using the
// shared http.Client (h2c/gRPC). baseURL e.g. "http://entitlements:8080".
func New(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	svc := entitlementsv1connect.NewEntitlementsServiceClient(httpClient, baseURL, opts...)
	return &Client{svc: svc}
}

// NewFromClient wraps an already-built generated client (useful for tests/wiring).
func NewFromClient(svc entitlementsv1connect.EntitlementsServiceClient) *Client {
	return &Client{svc: svc}
}

// ReserveQuota implements ports.Entitlements.
func (c *Client) ReserveQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) (ports.ReserveResult, error) {
	resp, err := c.svc.ReserveQuota(ctx, connect.NewRequest(&entitlementsv1.ReserveQuotaRequest{
		OwnerId:       ownerID,
		Key:           key,
		Delta:         delta,
		ReservationId: reservationID,
	}))
	if err != nil {
		return ports.ReserveResult{}, err
	}
	res := ports.ReserveResult{OK: resp.Msg.GetOk(), Remaining: resp.Msg.GetRemaining()}
	if !res.OK {
		// Enrich with an upgrade hint from CheckQuota when the reservation was denied.
		if chk, cerr := c.svc.CheckQuota(ctx, connect.NewRequest(&entitlementsv1.CheckQuotaRequest{
			OwnerId: ownerID, Key: key, Delta: delta,
		})); cerr == nil {
			res.UpgradeHint = chk.Msg.GetUpgradeHint()
		}
	}
	return res, nil
}

// ReleaseQuota implements ports.Entitlements.
func (c *Client) ReleaseQuota(ctx context.Context, ownerID, key string, delta int64, reservationID string) error {
	_, err := c.svc.ReleaseQuota(ctx, connect.NewRequest(&entitlementsv1.ReleaseQuotaRequest{
		OwnerId:       ownerID,
		Key:           key,
		Delta:         delta,
		ReservationId: reservationID,
	}))
	return err
}

// HasFeature implements ports.Entitlements.
func (c *Client) HasFeature(ctx context.Context, ownerID, feature string) (bool, error) {
	resp, err := c.svc.HasFeature(ctx, connect.NewRequest(&entitlementsv1.HasFeatureRequest{
		OwnerId: ownerID,
		Feature: feature,
	}))
	if err != nil {
		return false, err
	}
	return resp.Msg.GetEnabled(), nil
}
