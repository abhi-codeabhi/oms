// Package grpc is the Connect handler for AggregatorsService. It maps proto
// requests to app use cases, app/domain types back to proto, and domain errors to
// Connect codes. The restaurant id ALWAYS comes from the JWT-derived tenancy
// scope, never the request body (CONVENTIONS.md). No business logic lives here.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	aggregatorsv1 "github.com/restorna/platform/gen/go/restorna/aggregators/v1"
	"github.com/restorna/platform/gen/go/restorna/aggregators/v1/aggregatorsv1connect"
	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/aggregators/internal/app"
	"github.com/restorna/platform/services/aggregators/internal/domain"
)

// Handler adapts *app.App to the generated AggregatorsServiceHandler interface.
type Handler struct {
	aggregatorsv1connect.UnimplementedAggregatorsServiceHandler
	uc *app.App
}

var _ aggregatorsv1connect.AggregatorsServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// PushMenu fetches the current menu from catalog, resolves the aggregator, and
// pushes. The connector_id in the request is an optional preference.
func (h *Handler) PushMenu(ctx context.Context, req *connect.Request[aggregatorsv1.PushMenuRequest]) (*connect.Response[aggregatorsv1.PushMenuResponse], error) {
	res, err := h.uc.PushMenu(ctx, restaurantFromCtx(ctx), req.Msg.GetConnectorId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&aggregatorsv1.PushMenuResponse{Ok: res.OK, Items: res.Items}), nil
}

// ListExternalOrders returns persisted external orders (optionally filtered).
func (h *Handler) ListExternalOrders(ctx context.Context, req *connect.Request[aggregatorsv1.ListExternalOrdersRequest]) (*connect.Response[aggregatorsv1.ListExternalOrdersResponse], error) {
	orders, err := h.uc.ListExternalOrders(ctx, restaurantFromCtx(ctx), req.Msg.GetConnectorId(), req.Msg.GetStatus())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&aggregatorsv1.ListExternalOrdersResponse{Orders: ordersToProto(orders)}), nil
}

// AckExternalOrder updates status (accept/reject/update) and pushes it upstream.
func (h *Handler) AckExternalOrder(ctx context.Context, req *connect.Request[aggregatorsv1.AckExternalOrderRequest]) (*connect.Response[aggregatorsv1.AckExternalOrderResponse], error) {
	o, err := h.uc.AckExternalOrder(ctx, restaurantFromCtx(ctx), req.Msg.GetExternalOrderId(), req.Msg.GetStatus())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&aggregatorsv1.AckExternalOrderResponse{Order: orderToProto(o)}), nil
}

// --- mapping helpers ---

func orderToProto(o domain.ExternalOrder) *aggregatorsv1.ExternalOrder {
	items := make([]*aggregatorsv1.ExternalOrder_Item, 0, len(o.Items))
	for _, it := range o.Items {
		items = append(items, &aggregatorsv1.ExternalOrder_Item{
			Name: it.Name,
			Qty:  it.Qty,
			Price: &commonv1.Money{
				Minor:    it.Price.Minor,
				Currency: it.Price.Currency,
			},
		})
	}
	return &aggregatorsv1.ExternalOrder{
		Id:          o.ID,
		ConnectorId: o.ConnectorID,
		ExternalRef: o.ExternalRef,
		Status:      string(o.Status),
		Items:       items,
		PlacedAt:    o.PlacedAt,
	}
}

func ordersToProto(in []domain.ExternalOrder) []*aggregatorsv1.ExternalOrder {
	out := make([]*aggregatorsv1.ExternalOrder, 0, len(in))
	for _, o := range in {
		out = append(out, orderToProto(o))
	}
	return out
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, pkgerrors.ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth
// interceptor. The restaurant id (the tenant key) ALWAYS comes from the auth
// context, never the request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
