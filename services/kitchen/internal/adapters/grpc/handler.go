// Package grpc is the Connect handler for KitchenService. It maps proto requests
// to app use cases, app/domain types back to proto, and domain errors to Connect
// codes. The restaurant id ALWAYS comes from the JWT-derived tenancy scope, never
// the request body (CONVENTIONS.md). No business logic lives here (map only).
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	kitchenv1 "github.com/restorna/platform/gen/go/restorna/kitchen/v1"
	"github.com/restorna/platform/gen/go/restorna/kitchen/v1/kitchenv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/kitchen/internal/app"
	"github.com/restorna/platform/services/kitchen/internal/domain"
)

// Handler adapts *app.App to the generated KitchenServiceHandler interface.
type Handler struct {
	kitchenv1connect.UnimplementedKitchenServiceHandler
	uc *app.App
}

var _ kitchenv1connect.KitchenServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) ReceiveTicket(ctx context.Context, req *connect.Request[kitchenv1.ReceiveTicketRequest]) (*connect.Response[kitchenv1.ReceiveTicketResponse], error) {
	items := make([]app.ReceiveItemInput, 0, len(req.Msg.GetItems()))
	for _, it := range req.Msg.GetItems() {
		items = append(items, app.ReceiveItemInput{Name: it.GetName(), Station: it.GetStation()})
	}
	t, err := h.uc.ReceiveTicket(ctx, restaurantFromCtx(ctx), app.ReceiveTicketInput{
		OrderID: req.Msg.GetOrderId(),
		Table:   req.Msg.GetTable(),
		Items:   items,
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.ReceiveTicketResponse{Ticket: ticketToProto(t)}), nil
}

func (h *Handler) AdvanceItem(ctx context.Context, req *connect.Request[kitchenv1.AdvanceItemRequest]) (*connect.Response[kitchenv1.AdvanceItemResponse], error) {
	t, err := h.uc.AdvanceItem(ctx, restaurantFromCtx(ctx), req.Msg.GetTicketId(), int(req.Msg.GetItemIndex()))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.AdvanceItemResponse{Ticket: ticketToProto(t)}), nil
}

func (h *Handler) Bump(ctx context.Context, req *connect.Request[kitchenv1.BumpRequest]) (*connect.Response[kitchenv1.BumpResponse], error) {
	t, err := h.uc.Bump(ctx, restaurantFromCtx(ctx), req.Msg.GetTicketId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.BumpResponse{Ticket: ticketToProto(t)}), nil
}

func (h *Handler) Serve(ctx context.Context, req *connect.Request[kitchenv1.ServeRequest]) (*connect.Response[kitchenv1.ServeResponse], error) {
	t, err := h.uc.Serve(ctx, restaurantFromCtx(ctx), req.Msg.GetTicketId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.ServeResponse{Ticket: ticketToProto(t)}), nil
}

func (h *Handler) GetBoard(ctx context.Context, _ *connect.Request[kitchenv1.GetBoardRequest]) (*connect.Response[kitchenv1.GetBoardResponse], error) {
	tickets, err := h.uc.GetBoard(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.GetBoardResponse{Tickets: ticketsToProto(tickets)}), nil
}

func (h *Handler) ServeQueue(ctx context.Context, _ *connect.Request[kitchenv1.ServeQueueRequest]) (*connect.Response[kitchenv1.ServeQueueResponse], error) {
	tickets, err := h.uc.ServeQueue(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.ServeQueueResponse{Tickets: ticketsToProto(tickets)}), nil
}

func (h *Handler) AllDay(ctx context.Context, _ *connect.Request[kitchenv1.AllDayRequest]) (*connect.Response[kitchenv1.AllDayResponse], error) {
	counts, err := h.uc.AllDay(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&kitchenv1.AllDayResponse{Counts: counts}), nil
}

// --- mapping helpers ---

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func ticketToProto(t domain.Ticket) *kitchenv1.Ticket {
	items := make([]*kitchenv1.TicketItem, 0, len(t.Items))
	for _, it := range t.Items {
		items = append(items, &kitchenv1.TicketItem{
			Id:      it.ID,
			Name:    it.Name,
			Station: it.Station,
			State:   int32(it.State),
		})
	}
	return &kitchenv1.Ticket{
		Id:        t.ID,
		OrderId:   t.OrderID,
		Table:     t.Table,
		Items:     items,
		Served:    t.Served,
		CreatedAt: t.CreatedAt.UTC().Format(rfc3339),
	}
}

func ticketsToProto(in []domain.Ticket) []*kitchenv1.Ticket {
	out := make([]*kitchenv1.Ticket, 0, len(in))
	for _, t := range in {
		out = append(out, ticketToProto(t))
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
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth
// interceptor. The restaurant id (the KDS tenant key) ALWAYS comes from the auth
// context, never the request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
