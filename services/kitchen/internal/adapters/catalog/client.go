// Package catalog adapts the generated CatalogService Connect client to the app's
// ports.MenuResolver interface. This is the only place that knows about the
// generated catalog client; the app stays infra-free. Used by the OrderPlaced
// consumer to resolve a menu item's display name + kitchen station before firing
// a ticket (catalog is the source of truth for routing).
package catalog

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	catalogv1 "github.com/restorna/platform/gen/go/restorna/catalog/v1"
	"github.com/restorna/platform/gen/go/restorna/catalog/v1/catalogv1connect"
	"github.com/restorna/platform/services/kitchen/internal/ports"
)

// Client implements ports.MenuResolver over a Connect CatalogService client.
type Client struct {
	svc catalogv1connect.CatalogServiceClient
}

var _ ports.MenuResolver = (*Client)(nil)

// New builds a Client talking to the catalog service at baseURL using the shared
// http.Client (h2c/gRPC). baseURL e.g. "http://catalog:8080".
func New(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	svc := catalogv1connect.NewCatalogServiceClient(httpClient, baseURL, opts...)
	return &Client{svc: svc}
}

// NewFromClient wraps an already-built generated client (useful for tests/wiring).
func NewFromClient(svc catalogv1connect.CatalogServiceClient) *Client {
	return &Client{svc: svc}
}

// Resolve implements ports.MenuResolver. CatalogService scopes the lookup to the
// caller's restaurant from the auth context; the consumer sets that scope (and the
// internal service token) on ctx before calling, so we never trust an id from the
// event body for tenancy. The restaurantID argument is carried for symmetry and
// future per-tenant routing.
func (c *Client) Resolve(ctx context.Context, _ string, itemID string) (ports.ResolvedItem, error) {
	resp, err := c.svc.GetItem(ctx, connect.NewRequest(&catalogv1.GetItemRequest{ItemId: itemID}))
	if err != nil {
		return ports.ResolvedItem{}, err
	}
	item := resp.Msg.GetItem()
	if item == nil {
		return ports.ResolvedItem{}, nil
	}
	return ports.ResolvedItem{Name: item.GetName(), Station: item.GetStation()}, nil
}
