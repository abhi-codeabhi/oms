// Package clients holds the generated-client adapters to OTHER services. They are
// the ONLY place that knows about the ordering/catalog/settings/promotions Connect
// clients; the app stays infra-free behind its ports. Each adapter scopes calls to
// the caller's restaurant via the auth context (or, for the consumers, the trusted
// event envelope set on ctx).
package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	orderingv1 "github.com/restorna/platform/gen/go/restorna/ordering/v1"
	"github.com/restorna/platform/gen/go/restorna/ordering/v1/orderingv1connect"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Ordering implements ports.Orders over a Connect OrderingService client.
type Ordering struct {
	svc orderingv1connect.OrderingServiceClient
}

var _ ports.Orders = (*Ordering)(nil)

// NewOrdering builds an Ordering client talking to baseURL (e.g. "http://ordering:8080").
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

// ListForTable returns the table's UNBILLED orders (include_billed=false).
func (o *Ordering) ListForTable(ctx context.Context, _ string, table string) ([]ports.Order, error) {
	resp, err := o.svc.ListForTable(ctx, connect.NewRequest(&orderingv1.ListForTableRequest{
		Table:         table,
		IncludeBilled: false,
	}))
	if err != nil {
		return nil, err
	}
	out := make([]ports.Order, 0, len(resp.Msg.GetOrders()))
	for _, ord := range resp.Msg.GetOrders() {
		lines := make([]ports.OrderLine, 0, len(ord.GetLines()))
		for _, ln := range ord.GetLines() {
			lines = append(lines, ports.OrderLine{
				MenuItemID: ln.GetMenuItemId(),
				Name:       ln.GetName(),
				Qty:        ln.GetQty(),
				UnitPrice:  fromProtoMoney(ln.GetUnitPrice()),
			})
		}
		out = append(out, ports.Order{ID: ord.GetId(), Table: ord.GetTableId(), Lines: lines})
	}
	return out, nil
}

// MarkBilled flags the given orders billed so they can't be billed twice.
func (o *Ordering) MarkBilled(ctx context.Context, _ string, orderIDs []string) error {
	if len(orderIDs) == 0 {
		return nil
	}
	_, err := o.svc.MarkBilled(ctx, connect.NewRequest(&orderingv1.MarkBilledRequest{OrderIds: orderIDs}))
	return err
}

// fromProtoMoney converts a common.v1.Money to pkg/money.Money (nil-safe).
func fromProtoMoney(m *commonv1.Money) money.Money {
	if m == nil {
		return money.Money{}
	}
	return money.New(m.GetMinor(), m.GetCurrency())
}
