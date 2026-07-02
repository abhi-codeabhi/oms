// Package connectorhub adapts the generated ConnectorHubService Connect client to
// the app's ports.ConnectorHub interface. This is the only place that knows about
// the generated connector client; the app stays infra-free. Used by CreateIntent/
// Capture/Refund to resolve the active payment provider + its decrypted config.
package connectorhub

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	connectorv1 "github.com/restorna/platform/gen/go/restorna/connector/v1"
	"github.com/restorna/platform/gen/go/restorna/connector/v1/connectorv1connect"
	"github.com/restorna/platform/services/payments/internal/ports"
)

// Client implements ports.ConnectorHub over a Connect ConnectorHubService client.
type Client struct {
	svc connectorv1connect.ConnectorHubServiceClient
}

var _ ports.ConnectorHub = (*Client)(nil)

// New builds a Client talking to connector-hub at baseURL using the shared
// http.Client (h2c/gRPC). baseURL e.g. "http://connector-hub:8080".
func New(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	svc := connectorv1connect.NewConnectorHubServiceClient(httpClient, baseURL, opts...)
	return &Client{svc: svc}
}

// NewFromClient wraps an already-built generated client (useful for tests/wiring).
func NewFromClient(svc connectorv1connect.ConnectorHubServiceClient) *Client {
	return &Client{svc: svc}
}

// ResolvePayment resolves the active CAPABILITY_PAYMENT connector for the caller's
// restaurant. ConnectorHubService scopes to the auth context; the tenant scope is
// carried on ctx (never trusted from a body). preferConnectorID is an optional
// hint. Returns the connector id + decrypted config to instantiate the adapter.
func (c *Client) ResolvePayment(ctx context.Context, _ string, preferConnectorID string) (ports.Resolved, error) {
	resp, err := c.svc.Resolve(ctx, connect.NewRequest(&connectorv1.ResolveRequest{
		Capability:        connectorv1.Capability_CAPABILITY_PAYMENT,
		PreferConnectorId: preferConnectorID,
	}))
	if err != nil {
		return ports.Resolved{}, err
	}
	return ports.Resolved{
		ConnectorID:    resp.Msg.GetConnectorId(),
		InstallationID: resp.Msg.GetInstallationId(),
		TestMode:       resp.Msg.GetTestMode(),
		Config:         resp.Msg.GetConfig(),
	}, nil
}
