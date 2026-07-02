// Package grpc is the Connect handler for FloorService. It maps proto requests to
// app use cases, app/domain types back to proto, and domain errors to Connect
// codes. The restaurant id ALWAYS comes from the JWT-derived tenancy scope, never
// the request body (CONVENTIONS.md). No business logic lives here (map only).
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	floorv1 "github.com/restorna/platform/gen/go/restorna/floor/v1"
	"github.com/restorna/platform/gen/go/restorna/floor/v1/floorv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/floor/internal/app"
	"github.com/restorna/platform/services/floor/internal/domain"
)

// Handler adapts *app.App to the generated FloorServiceHandler interface.
type Handler struct {
	floorv1connect.UnimplementedFloorServiceHandler
	uc *app.App
}

var _ floorv1connect.FloorServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) InitFloor(ctx context.Context, req *connect.Request[floorv1.InitFloorRequest]) (*connect.Response[floorv1.InitFloorResponse], error) {
	f, err := h.uc.InitFloor(ctx, restaurantFromCtx(ctx), req.Msg.GetTables())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.InitFloorResponse{Floor: floorToProto(f)}), nil
}

func (h *Handler) GetFloor(ctx context.Context, _ *connect.Request[floorv1.GetFloorRequest]) (*connect.Response[floorv1.GetFloorResponse], error) {
	f, err := h.uc.GetFloor(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.GetFloorResponse{Floor: floorToProto(f)}), nil
}

func (h *Handler) SeatParty(ctx context.Context, req *connect.Request[floorv1.SeatPartyRequest]) (*connect.Response[floorv1.SeatPartyResponse], error) {
	f, err := h.uc.SeatParty(ctx, restaurantFromCtx(ctx), req.Msg.GetN())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.SeatPartyResponse{Floor: floorToProto(f)}), nil
}

func (h *Handler) AssignWaiter(ctx context.Context, req *connect.Request[floorv1.AssignWaiterRequest]) (*connect.Response[floorv1.AssignWaiterResponse], error) {
	f, err := h.uc.AssignWaiter(ctx, restaurantFromCtx(ctx), req.Msg.GetN(), req.Msg.GetWaiterId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.AssignWaiterResponse{Floor: floorToProto(f)}), nil
}

func (h *Handler) Move(ctx context.Context, req *connect.Request[floorv1.MoveRequest]) (*connect.Response[floorv1.MoveResponse], error) {
	res, err := h.uc.Move(ctx, restaurantFromCtx(ctx), req.Msg.GetSrc(), req.Msg.GetDst())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.MoveResponse{Floor: floorToProto(res.Floor), Verb: res.Verb}), nil
}

func (h *Handler) GetNudges(ctx context.Context, _ *connect.Request[floorv1.GetNudgesRequest]) (*connect.Response[floorv1.GetNudgesResponse], error) {
	nudges, err := h.uc.GetNudges(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.GetNudgesResponse{Nudges: nudgesToProto(nudges)}), nil
}

func (h *Handler) AckNudge(ctx context.Context, req *connect.Request[floorv1.AckNudgeRequest]) (*connect.Response[floorv1.AckNudgeResponse], error) {
	if err := h.uc.AckNudge(ctx, restaurantFromCtx(ctx), req.Msg.GetN(), req.Msg.GetType()); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&floorv1.AckNudgeResponse{}), nil
}

// --- mapping helpers ---

func floorToProto(f domain.Floor) *floorv1.Floor {
	tables := make([]*floorv1.Table, 0, len(f.Tables))
	for _, t := range f.Tables {
		tables = append(tables, &floorv1.Table{
			N:             t.N,
			Status:        t.Status,
			Order:         t.Order,
			WaiterId:      t.WaiterID,
			SeatedAt:      t.SeatedAt,
			GreetedAt:     t.GreetedAt,
			LastServedAt:  t.LastServedAt,
			LastCheckinAt: t.LastCheckinAt,
		})
	}
	return &floorv1.Floor{Tables: tables}
}

func nudgesToProto(in []domain.Nudge) []*floorv1.Nudge {
	out := make([]*floorv1.Nudge, 0, len(in))
	for _, n := range in {
		out = append(out, &floorv1.Nudge{
			Table: n.Table,
			Type:  n.Type,
			Label: n.Label,
			Since: n.Since,
		})
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
// interceptor. The restaurant id (the floor tenant key) ALWAYS comes from the auth
// context, never the request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
