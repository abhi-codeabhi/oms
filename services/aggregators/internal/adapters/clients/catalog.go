// Package clients holds the generated-client adapters to OTHER services
// (catalog, ordering, connector-hub). These are the only places that know about
// the generated Connect clients; the app stays infra-free and talks to the
// ports interfaces they implement.
package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	catalogv1 "github.com/restorna/platform/gen/go/restorna/catalog/v1"
	"github.com/restorna/platform/gen/go/restorna/catalog/v1/catalogv1connect"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

// Catalog implements ports.Catalog over a Connect CatalogService client. It uses
// ListAllItems (manager view, includes unavailable) so the aggregator menu push
// carries the full menu and can 86 items via the Available flag.
type Catalog struct {
	svc catalogv1connect.CatalogServiceClient
}

var _ ports.Catalog = (*Catalog)(nil)

// NewCatalog builds a Catalog client talking to the catalog service at baseURL.
func NewCatalog(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Catalog {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Catalog{svc: catalogv1connect.NewCatalogServiceClient(httpClient, baseURL, opts...)}
}

// NewCatalogFromClient wraps an already-built generated client (tests/wiring).
func NewCatalogFromClient(svc catalogv1connect.CatalogServiceClient) *Catalog {
	return &Catalog{svc: svc}
}

// ListAllItems implements ports.Catalog. CatalogService scopes the lookup to the
// caller's restaurant from the auth context; PushMenu sets that scope on ctx
// before calling. The restaurantID argument is carried for symmetry.
func (c *Catalog) ListAllItems(ctx context.Context, _ string) ([]ports.MenuItem, error) {
	resp, err := c.svc.ListAllItems(ctx, connect.NewRequest(&catalogv1.ListAllItemsRequest{}))
	if err != nil {
		return nil, err
	}
	items := resp.Msg.GetItems()
	out := make([]ports.MenuItem, 0, len(items))
	for _, it := range items {
		mi := ports.MenuItem{
			ID:          it.GetId(),
			CategoryID:  it.GetCategoryId(),
			Name:        it.GetName(),
			Description: it.GetDescription(),
			Veg:         it.GetVeg(),
			Available:   it.GetAvailable(),
			Station:     it.GetStation(),
		}
		if p := it.GetPrice(); p != nil {
			mi.PriceMinor = p.GetMinor()
			mi.Currency = p.GetCurrency()
		}
		out = append(out, mi)
	}
	return out, nil
}
