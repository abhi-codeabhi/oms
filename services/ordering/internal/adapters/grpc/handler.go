// Package grpc is the Connect handler for OrderingService. It maps proto requests
// to app use cases, app/domain types back to proto, and domain errors to Connect
// codes. No business logic lives here (CONVENTIONS.md: map only). The restaurant
// id ALWAYS comes from the JWT-derived tenancy scope, never the request body.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	orderingv1 "github.com/restorna/platform/gen/go/restorna/ordering/v1"
	"github.com/restorna/platform/gen/go/restorna/ordering/v1/orderingv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/ordering/internal/app"
	"github.com/restorna/platform/services/ordering/internal/domain"
)

// Handler adapts *app.App to the generated OrderingServiceHandler interface.
type Handler struct {
	orderingv1connect.UnimplementedOrderingServiceHandler
	uc *app.App
}

var _ orderingv1connect.OrderingServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) PlaceOrder(ctx context.Context, req *connect.Request[orderingv1.PlaceOrderRequest]) (*connect.Response[orderingv1.PlaceOrderResponse], error) {
	items := make([]domain.NewLineInput, 0, len(req.Msg.GetItems()))
	for _, it := range req.Msg.GetItems() {
		items = append(items, domain.NewLineInput{
			MenuItemID: it.GetMenuItemId(),
			Name:       it.GetName(),
			Qty:        it.GetQty(),
			UnitPrice:  moneyFromProto(it.GetUnitPrice()),
		})
	}
	o, err := h.uc.PlaceOrder(ctx, app.PlaceOrderInput{
		RestaurantID: restaurantFromCtx(ctx),
		TableID:      req.Msg.GetTableId(),
		Items:        items,
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&orderingv1.PlaceOrderResponse{Order: orderToProto(o)}), nil
}

func (h *Handler) GetOrder(ctx context.Context, req *connect.Request[orderingv1.GetOrderRequest]) (*connect.Response[orderingv1.GetOrderResponse], error) {
	o, err := h.uc.GetOrder(ctx, restaurantFromCtx(ctx), req.Msg.GetOrderId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&orderingv1.GetOrderResponse{Order: orderToProto(o)}), nil
}

func (h *Handler) ListForTable(ctx context.Context, req *connect.Request[orderingv1.ListForTableRequest]) (*connect.Response[orderingv1.ListForTableResponse], error) {
	orders, err := h.uc.ListForTable(ctx, restaurantFromCtx(ctx), req.Msg.GetTable(), req.Msg.GetIncludeBilled())
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*orderingv1.Order, 0, len(orders))
	for _, o := range orders {
		out = append(out, orderToProto(o))
	}
	return connect.NewResponse(&orderingv1.ListForTableResponse{Orders: out}), nil
}

func (h *Handler) MarkBilled(ctx context.Context, req *connect.Request[orderingv1.MarkBilledRequest]) (*connect.Response[orderingv1.MarkBilledResponse], error) {
	n, err := h.uc.MarkBilled(ctx, restaurantFromCtx(ctx), req.Msg.GetOrderIds())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&orderingv1.MarkBilledResponse{Count: int32(n)}), nil
}

func (h *Handler) Relocate(ctx context.Context, req *connect.Request[orderingv1.RelocateRequest]) (*connect.Response[orderingv1.RelocateResponse], error) {
	moved, err := h.uc.Relocate(ctx, restaurantFromCtx(ctx), req.Msg.GetFromTable(), req.Msg.GetToTable())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&orderingv1.RelocateResponse{Moved: int32(moved)}), nil
}

// --- mapping helpers ---

func orderToProto(o domain.Order) *orderingv1.Order {
	lines := make([]*orderingv1.Line, 0, len(o.Lines))
	for _, l := range o.Lines {
		lines = append(lines, &orderingv1.Line{
			Id:         l.ID,
			MenuItemId: l.MenuItemID,
			Name:       l.Name,
			Qty:        l.Qty,
			UnitPrice:  moneyToProto(l.UnitPrice),
			Station:    l.Station,
		})
	}
	return &orderingv1.Order{
		Id:           o.ID,
		RestaurantId: o.RestaurantID,
		TableId:      o.TableID,
		Lines:        lines,
		Subtotal:     moneyToProto(o.Subtotal),
		Billed:       o.Billed,
		CreatedAt:    o.CreatedAt.UTC().Format(rfc3339),
	}
}

func moneyToProto(m money.Money) *commonv1.Money {
	return &commonv1.Money{Minor: m.Minor, Currency: m.Currency}
}

func moneyFromProto(m *commonv1.Money) money.Money {
	if m == nil {
		return money.New(0, "INR")
	}
	ccy := m.GetCurrency()
	if ccy == "" {
		ccy = "INR"
	}
	return money.New(m.GetMinor(), ccy)
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

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
// interceptor. The restaurant id ALWAYS comes from the auth context, never the
// request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
