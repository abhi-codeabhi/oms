// Package grpc adapts the StaffService Connect contract to the app use cases. It
// maps proto <-> domain and translates domain/app errors into Connect codes. No
// business logic lives here.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	staffv1 "github.com/restorna/platform/gen/go/restorna/staff/v1"
	"github.com/restorna/platform/gen/go/restorna/staff/v1/staffv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/staff/internal/app"
	"github.com/restorna/platform/services/staff/internal/domain"
)

// Handler implements staffv1connect.StaffServiceHandler.
type Handler struct {
	uc *app.App
}

// compile-time assertion that we satisfy the generated interface.
var _ staffv1connect.StaffServiceHandler = (*Handler)(nil)

// New builds the Connect handler over the app use cases.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) AddStaff(ctx context.Context, req *connect.Request[staffv1.AddStaffRequest]) (*connect.Response[staffv1.AddStaffResponse], error) {
	m, err := h.uc.AddStaff(ctx, req.Msg.GetRestaurantId(), req.Msg.GetName(), req.Msg.GetEmail(), req.Msg.GetPhone(), req.Msg.GetRole())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&staffv1.AddStaffResponse{Member: toProto(m)}), nil
}

func (h *Handler) ListStaff(ctx context.Context, req *connect.Request[staffv1.ListStaffRequest]) (*connect.Response[staffv1.ListStaffResponse], error) {
	members, page, err := h.uc.ListStaff(ctx, req.Msg.GetRestaurantId(), req.Msg.GetPage())
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*staffv1.StaffMember, 0, len(members))
	for _, m := range members {
		out = append(out, toProto(m))
	}
	return connect.NewResponse(&staffv1.ListStaffResponse{Members: out, Page: page}), nil
}

func (h *Handler) SetStaffActive(ctx context.Context, req *connect.Request[staffv1.SetStaffActiveRequest]) (*connect.Response[staffv1.SetStaffActiveResponse], error) {
	m, err := h.uc.SetStaffActive(ctx, req.Msg.GetStaffId(), req.Msg.GetActive())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&staffv1.SetStaffActiveResponse{Member: toProto(m)}), nil
}

func (h *Handler) ChangeRole(ctx context.Context, req *connect.Request[staffv1.ChangeRoleRequest]) (*connect.Response[staffv1.ChangeRoleResponse], error) {
	m, err := h.uc.ChangeRole(ctx, req.Msg.GetStaffId(), req.Msg.GetRole())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&staffv1.ChangeRoleResponse{Member: toProto(m)}), nil
}

func (h *Handler) InviteStaff(ctx context.Context, req *connect.Request[staffv1.InviteStaffRequest]) (*connect.Response[staffv1.InviteStaffResponse], error) {
	inviteID, err := h.uc.InviteStaff(ctx, req.Msg.GetStaffId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&staffv1.InviteStaffResponse{InviteId: inviteID}), nil
}

// toProto maps a domain.Member to the wire message.
func toProto(m domain.Member) *staffv1.StaffMember {
	return &staffv1.StaffMember{
		Id:           m.ID,
		OwnerId:      m.OwnerID,
		BrandId:      m.BrandID,
		RestaurantId: m.RestaurantID,
		Name:         m.Name,
		Email:        m.Email,
		Phone:        m.Phone,
		Role:         m.Role,
		Active:       m.Active,
		UserId:       m.UserID,
	}
}

// toConnect maps domain/app errors to Connect status codes. The upgrade hint for
// a quota error is attached as the message so the owner sees it.
func toConnect(err error) *connect.Error {
	var qe app.ErrQuotaExceeded
	if errors.As(err, &qe) {
		msg := qe.Error()
		if qe.UpgradeHint != "" {
			msg = qe.UpgradeHint
		}
		ce := connect.NewError(connect.CodeResourceExhausted, errors.New(msg))
		if qe.UpgradeHint != "" {
			ce.Meta().Set("x-upgrade-hint", qe.UpgradeHint)
		}
		return ce
	}

	switch {
	case errors.Is(err, domain.ErrStaffNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrNotInScope):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrInvalidName),
		errors.Is(err, domain.ErrInvalidContact),
		errors.Is(err, domain.ErrInvalidRole):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrAlreadyLinked):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}

	// tenancy.Require returns a permission-denied error.
	if errors.Is(err, tenancy.ErrPermissionDenied) {
		return connect.NewError(connect.CodePermissionDenied, err)
	}

	return connect.NewError(connect.CodeInternal, err)
}
