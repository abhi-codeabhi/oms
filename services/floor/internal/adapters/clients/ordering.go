// Package clients: OrderingService -> ports.OrderRelocator. On a floor Move/Swap
// the floor calls Relocate so open orders follow the seat to the new table label.
package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	orderingv1 "github.com/restorna/platform/gen/go/restorna/ordering/v1"
	"github.com/restorna/platform/gen/go/restorna/ordering/v1/orderingv1connect"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// OrderingClient implements ports.OrderRelocator over a Connect OrderingService.
type OrderingClient struct {
	svc orderingv1connect.OrderingServiceClient
}

var _ ports.OrderRelocator = (*OrderingClient)(nil)

// NewOrdering builds an OrderingClient talking to the ordering service at baseURL.
func NewOrdering(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *OrderingClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &OrderingClient{svc: orderingv1connect.NewOrderingServiceClient(httpClient, baseURL, opts...)}
}

// NewOrderingFromClient wraps an already-built generated client (tests/wiring).
func NewOrderingFromClient(svc orderingv1connect.OrderingServiceClient) *OrderingClient {
	return &OrderingClient{svc: svc}
}

// Relocate moves all open orders from one table label to another, returning the
// count moved (OrderingService.Relocate).
func (c *OrderingClient) Relocate(ctx context.Context, _ string, fromTable, toTable string) (int, error) {
	resp, err := c.svc.Relocate(ctx, connect.NewRequest(&orderingv1.RelocateRequest{
		FromTable: fromTable,
		ToTable:   toTable,
	}))
	if err != nil {
		return 0, err
	}
	return int(resp.Msg.GetMoved()), nil
}
