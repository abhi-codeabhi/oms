// Package grpc is the Connect handler for ServiceRequestsService. It maps proto
// requests to app use cases, app/domain types back to proto, and domain errors to
// Connect codes. The restaurant id ALWAYS comes from the JWT-derived tenancy
// scope, never the request body (CONVENTIONS.md). No business logic lives here.
package grpc

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	servicerequestsv1 "github.com/restorna/platform/gen/go/restorna/servicerequests/v1"
	"github.com/restorna/platform/gen/go/restorna/servicerequests/v1/servicerequestsv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/servicerequests/internal/app"
	"github.com/restorna/platform/services/servicerequests/internal/domain"
)

// Handler adapts *app.App to the generated ServiceRequestsServiceHandler interface.
type Handler struct {
	servicerequestsv1connect.UnimplementedServiceRequestsServiceHandler
	uc *app.App
}

var _ servicerequestsv1connect.ServiceRequestsServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// Raise creates a request. Returns CodeFailedPrecondition when the table+type is
// still in its acknowledge cooldown window.
func (h *Handler) Raise(ctx context.Context, req *connect.Request[servicerequestsv1.RaiseRequest]) (*connect.Response[servicerequestsv1.RaiseResponse], error) {
	r, err := h.uc.Raise(ctx, restaurantFromCtx(ctx), app.RaiseInput{
		Type:       req.Msg.GetType(),
		Table:      req.Msg.GetTable(),
		AssignedTo: req.Msg.GetAssignedTo(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&servicerequestsv1.RaiseResponse{Request: requestToProto(r)}), nil
}

// ListOpen returns every request not yet done (assigned + escalated).
func (h *Handler) ListOpen(ctx context.Context, _ *connect.Request[servicerequestsv1.ListOpenRequest]) (*connect.Response[servicerequestsv1.ListOpenResponse], error) {
	reqs, err := h.uc.ListOpen(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&servicerequestsv1.ListOpenResponse{Requests: requestsToProto(reqs)}), nil
}

// Acknowledge marks a request done and records the table+type cooldown.
func (h *Handler) Acknowledge(ctx context.Context, req *connect.Request[servicerequestsv1.AcknowledgeRequest]) (*connect.Response[servicerequestsv1.AcknowledgeResponse], error) {
	r, err := h.uc.Acknowledge(ctx, restaurantFromCtx(ctx), req.Msg.GetRequestId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&servicerequestsv1.AcknowledgeResponse{Request: requestToProto(r)}), nil
}

// EscalateDue flips assigned requests past the threshold to escalated. `now` is
// epoch ms from the request (zero -> the server clock).
func (h *Handler) EscalateDue(ctx context.Context, req *connect.Request[servicerequestsv1.EscalateDueRequest]) (*connect.Response[servicerequestsv1.EscalateDueResponse], error) {
	var now time.Time
	if ms := req.Msg.GetNow(); ms > 0 {
		now = time.UnixMilli(ms).UTC()
	}
	reqs, err := h.uc.EscalateDue(ctx, restaurantFromCtx(ctx), now)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&servicerequestsv1.EscalateDueResponse{Escalated: requestsToProto(reqs)}), nil
}

// --- mapping helpers ---

func requestToProto(r domain.Request) *servicerequestsv1.Request {
	return &servicerequestsv1.Request{
		Id:         r.ID,
		Type:       string(r.Type),
		Table:      r.Table,
		State:      string(r.State),
		AssignedTo: r.AssignedTo,
		CreatedAt:  r.CreatedAt.UTC().UnixMilli(),
	}
}

func requestsToProto(in []domain.Request) []*servicerequestsv1.Request {
	out := make([]*servicerequestsv1.Request, 0, len(in))
	for _, r := range in {
		out = append(out, requestToProto(r))
	}
	return out
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
// A cooldown rejection is a FailedPrecondition per the proto contract.
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrCooldown):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
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
