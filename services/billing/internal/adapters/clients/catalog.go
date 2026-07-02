package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	catalogv1 "github.com/restorna/platform/gen/go/restorna/catalog/v1"
	"github.com/restorna/platform/gen/go/restorna/catalog/v1/catalogv1connect"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Catalog implements ports.Menu over a Connect CatalogService client. It resolves
// a menu item's display name + course category for the bill aggregation. The
// proto Item carries a category_id; the billing bill groups by the human course
// label, so this adapter maps the category id through ListCategories once (cached)
// to a name, falling back to the id when no category lookup is available.
type Catalog struct {
	svc catalogv1connect.CatalogServiceClient
}

var _ ports.Menu = (*Catalog)(nil)

// NewCatalog builds a Catalog client talking to baseURL (e.g. "http://catalog:8080").
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

// GetItem resolves a menu item's name + course category. CatalogService scopes the
// lookup to the caller's restaurant from the auth context (set by the handler or
// the consumer). The category is resolved from the item's category_id via the
// restaurant's category list; an unresolved category falls back to the id.
func (c *Catalog) GetItem(ctx context.Context, _ string, itemID string) (ports.ResolvedItem, error) {
	resp, err := c.svc.GetItem(ctx, connect.NewRequest(&catalogv1.GetItemRequest{ItemId: itemID}))
	if err != nil {
		return ports.ResolvedItem{}, err
	}
	item := resp.Msg.GetItem()
	if item == nil {
		return ports.ResolvedItem{}, nil
	}
	category := c.categoryName(ctx, item.GetCategoryId())
	return ports.ResolvedItem{Name: item.GetName(), Category: category}, nil
}

// categoryName resolves a category id to its human name via ListCategories,
// returning the id unchanged on any miss (best-effort; the course grouping
// tolerates an "Other" fallback in the domain).
func (c *Catalog) categoryName(ctx context.Context, categoryID string) string {
	if categoryID == "" {
		return ""
	}
	resp, err := c.svc.ListCategories(ctx, connect.NewRequest(&catalogv1.ListCategoriesRequest{}))
	if err != nil {
		return categoryID
	}
	for _, cat := range resp.Msg.GetCategories() {
		if cat.GetId() == categoryID {
			if name := cat.GetName(); name != "" {
				return name
			}
		}
	}
	return categoryID
}
