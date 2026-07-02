// Package grpc adapts the OnboardingService Connect contract to the app saga. It
// maps proto <-> domain and translates domain/app errors into Connect codes. No
// business logic lives here.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	"github.com/restorna/platform/gen/go/restorna/onboarding/v1/onboardingv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/onboarding/internal/app"
	"github.com/restorna/platform/services/onboarding/internal/domain"
)

// Handler implements onboardingv1connect.OnboardingServiceHandler.
type Handler struct {
	uc *app.App
}

// compile-time assertion that we satisfy the generated interface.
var _ onboardingv1connect.OnboardingServiceHandler = (*Handler)(nil)

// New builds the Connect handler over the saga use cases.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

func (h *Handler) StartOnboarding(ctx context.Context, req *connect.Request[onboardingv1.StartOnboardingRequest]) (*connect.Response[onboardingv1.StartOnboardingResponse], error) {
	st, err := h.uc.StartOnboarding(ctx, app.StartInput{
		OwnerName:    req.Msg.GetOwnerName(),
		ContactEmail: req.Msg.GetContactEmail(),
		ContactPhone: req.Msg.GetContactPhone(),
		Country:      req.Msg.GetCountry(),
		PlanID:       req.Msg.GetPlanId(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&onboardingv1.StartOnboardingResponse{State: toProto(st)}), nil
}

func (h *Handler) SubmitBrand(ctx context.Context, req *connect.Request[onboardingv1.SubmitBrandRequest]) (*connect.Response[onboardingv1.SubmitBrandResponse], error) {
	st, brandID, err := h.uc.SubmitBrand(ctx, app.SubmitBrandInput{
		OnboardingID:    req.Msg.GetOnboardingId(),
		BrandName:       req.Msg.GetBrandName(),
		PrimaryColor:    req.Msg.GetPrimaryColor(),
		Logo:            req.Msg.GetLogo(),
		LogoContentType: req.Msg.GetLogoContentType(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&onboardingv1.SubmitBrandResponse{State: toProto(st), BrandId: brandID}), nil
}

func (h *Handler) SubmitOutlet(ctx context.Context, req *connect.Request[onboardingv1.SubmitOutletRequest]) (*connect.Response[onboardingv1.SubmitOutletResponse], error) {
	st, restaurantID, err := h.uc.SubmitOutlet(ctx, app.SubmitOutletInput{
		OnboardingID: req.Msg.GetOnboardingId(),
		Name:         req.Msg.GetName(),
		Address:      req.Msg.GetAddress(),
		Timezone:     req.Msg.GetTimezone(),
		GSTIN:        req.Msg.GetGstin(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&onboardingv1.SubmitOutletResponse{State: toProto(st), RestaurantId: restaurantID}), nil
}

func (h *Handler) InviteTeam(ctx context.Context, req *connect.Request[onboardingv1.InviteTeamRequest]) (*connect.Response[onboardingv1.InviteTeamResponse], error) {
	in := make([]app.InviteInput, 0, len(req.Msg.GetInvites()))
	for _, iv := range req.Msg.GetInvites() {
		in = append(in, app.InviteInput{
			Name:  iv.GetName(),
			Email: iv.GetEmail(),
			Phone: iv.GetPhone(),
			Role:  iv.GetRole(),
		})
	}
	st, results, err := h.uc.InviteTeam(ctx, req.Msg.GetOnboardingId(), in)
	if err != nil {
		return nil, toConnect(err)
	}
	// The proto InviteTeamResponse carries only the state; per-invite failures are
	// surfaced as response trailers so the proto contract need not change, while
	// the saga state still advances.
	resp := connect.NewResponse(&onboardingv1.InviteTeamResponse{State: toProto(st)})
	for _, r := range results {
		if !r.Invited {
			resp.Header().Add("x-onboarding-invite-failed", r.Email+": "+r.Error)
		}
	}
	return resp, nil
}

func (h *Handler) Complete(ctx context.Context, req *connect.Request[onboardingv1.CompleteRequest]) (*connect.Response[onboardingv1.CompleteResponse], error) {
	st, err := h.uc.Complete(ctx, req.Msg.GetOnboardingId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&onboardingv1.CompleteResponse{State: toProto(st)}), nil
}

func (h *Handler) GetState(ctx context.Context, req *connect.Request[onboardingv1.GetStateRequest]) (*connect.Response[onboardingv1.GetStateResponse], error) {
	st, err := h.uc.GetState(ctx, req.Msg.GetOnboardingId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&onboardingv1.GetStateResponse{State: toProto(st)}), nil
}

// toProto maps a domain.State to the wire OnboardingState message.
func toProto(s domain.State) *onboardingv1.OnboardingState {
	return &onboardingv1.OnboardingState{
		Id:        s.ID,
		OwnerId:   s.OwnerID,
		Current:   s.Current(),
		Completed: s.Completed(),
		Done:      s.Done,
	}
}

// toConnect maps domain/app errors to Connect status codes.
func toConnect(err error) *connect.Error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalidInput):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrNotInScope):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrOutOfOrder):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, domain.ErrAlreadyDone):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, tenancy.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	return connect.NewError(connect.CodeInternal, err)
}
