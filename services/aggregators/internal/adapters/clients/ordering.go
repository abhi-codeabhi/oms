package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	orderingv1 "github.com/restorna/platform/gen/go/restorna/ordering/v1"
	"github.com/restorna/platform/gen/go/restorna/ordering/v1/orderingv1connect"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

// Ordering implements ports.Ordering over a Connect OrderingService client. It
// forwards an ingested aggregator order into the OMS via PlaceOrder at the
// synthetic table so it flows to the kitchen like any dine-in order.
type Ordering struct {
	svc orderingv1connect.OrderingServiceClient
}

var _ ports.Ordering = (*Ordering)(nil)

// NewOrdering builds an Ordering client talking to the ordering service at baseURL.
func NewOrdering(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Ordering {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Ordering{svc: orderingv1connect.NewOrderingServiceClient(httpClient, baseURL, opts...)}
}

// NewOrderingFromClient wraps an already-built generated client (tests/wiring).
func NewOrderingFromClient(svc orderingv1connect.OrderingServiceClient) *Ordering {
	return &Ordering{svc: svc}
}

// PlaceOrder implements ports.Ordering. OrderingService scopes the write to the
// restaurant from the auth context; the consumer sets that scope on ctx before
// calling (derived from the trusted event envelope, never a request body).
func (o *Ordering) PlaceOrder(ctx context.Context, _ string, table string, lines []ports.OrderLine) (string, error) {
	items := make([]*orderingv1.PlaceOrderRequest_NewLine, 0, len(lines))
	for _, ln := range lines {
		items = append(items, &orderingv1.PlaceOrderRequest_NewLine{
			MenuItemId: ln.MenuItemID,
			Qty:        ln.Qty,
			Name:       ln.Name,
			UnitPrice: &commonv1.Money{
				Minor:    ln.PriceMinor,
				Currency: ln.Currency,
			},
		})
	}
	resp, err := o.svc.PlaceOrder(ctx, connect.NewRequest(&orderingv1.PlaceOrderRequest{
		TableId: table,
		Items:   items,
	}))
	if err != nil {
		return "", err
	}
	return resp.Msg.GetOrder().GetId(), nil
}
