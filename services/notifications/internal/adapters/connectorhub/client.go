// Package connectorhub adapts the generated ConnectorHubService Connect client to
// the app's ports.ConnectorHub interface. This is the only place that knows about
// the generated client; the app stays infra-free. It resolves the active
// notification provider (CAPABILITY_NOTIFICATION) + decrypted config for a tenant.
package connectorhub

import (
	"context"
	"errors"
	"net/http"

	"connectrpc.com/connect"

	connectorv1 "github.com/restorna/platform/gen/go/restorna/connector/v1"
	"github.com/restorna/platform/gen/go/restorna/connector/v1/connectorv1connect"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

// Client implements ports.ConnectorHub over a Connect ConnectorHubService client.
type Client struct {
	svc connectorv1connect.ConnectorHubServiceClient
}

var _ ports.ConnectorHub = (*Client)(nil)

// New builds a Client talking to the connector-hub at baseURL using the shared
// http.Client (h2c/gRPC). baseURL e.g. "http://connectorhub:8080".
func New(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Client{svc: connectorv1connect.NewConnectorHubServiceClient(httpClient, baseURL, opts...)}
}

// NewFromClient wraps an already-built generated client (useful for tests/wiring).
func NewFromClient(svc connectorv1connect.ConnectorHubServiceClient) *Client {
	return &Client{svc: svc}
}

// Resolve asks the hub for the active notification provider at this tenant. A
// NotFound (no provider installed) is normalized to Resolution{Installed:false} so
// the app can fall back to the built-in mock rather than treating it as an error.
func (c *Client) Resolve(ctx context.Context, ownerID string) (ports.Resolution, error) {
	resp, err := c.svc.Resolve(ctx, connect.NewRequest(&connectorv1.ResolveRequest{
		Capability: connectorv1.Capability_CAPABILITY_NOTIFICATION,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound || connect.CodeOf(err) == connect.CodeFailedPrecondition {
			return ports.Resolution{Installed: false}, nil
		}
		return ports.Resolution{}, err
	}
	connectorID := resp.Msg.GetConnectorId()
	return ports.Resolution{
		ConnectorID:    connectorID,
		InstallationID: resp.Msg.GetInstallationId(),
		TestMode:       resp.Msg.GetTestMode(),
		Config:         resp.Msg.GetConfig(),
		Installed:      connectorID != "",
	}, nil
}

// ErrNoProvider is returned by callers to signal that no provider is installed; kept
// here so the adapter and app share the same sentinel where needed.
var ErrNoProvider = errors.New("connectorhub: no notification provider installed")
