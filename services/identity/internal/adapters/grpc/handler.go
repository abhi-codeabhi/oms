// Package grpc is the Connect handler for IdentityService. It maps proto
// requests <-> domain/app calls and translates domain errors to Connect codes
// via pkg/errors.ToConnect. No business logic lives here.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/gen/go/restorna/identity/v1/identityv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"

	"github.com/restorna/platform/services/identity/internal/app"
	"github.com/restorna/platform/services/identity/internal/domain"
)

// Handler adapts the app.Service to the generated Connect interface.
type Handler struct {
	svc *app.Service
}

// New builds the Connect handler.
func New(svc *app.Service) *Handler { return &Handler{svc: svc} }

// compile-time check against the generated server interface.
var _ identityv1connect.IdentityServiceHandler = (*Handler)(nil)

func (h *Handler) StartOtp(ctx context.Context, req *connect.Request[identityv1.StartOtpRequest]) (*connect.Response[identityv1.StartOtpResponse], error) {
	m := req.Msg
	id, err := h.svc.StartOtp(ctx, m.GetChannel(), m.GetAddress(), m.GetRealm())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.StartOtpResponse{ChallengeId: id}), nil
}

func (h *Handler) VerifyOtp(ctx context.Context, req *connect.Request[identityv1.VerifyOtpRequest]) (*connect.Response[identityv1.VerifyOtpResponse], error) {
	pair, user, err := h.svc.VerifyOtp(ctx, req.Msg.GetChallengeId(), req.Msg.GetCode())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.VerifyOtpResponse{
		Tokens: toTokenPair(pair),
		User:   toUserProto(user),
	}), nil
}

func (h *Handler) Refresh(ctx context.Context, req *connect.Request[identityv1.RefreshRequest]) (*connect.Response[identityv1.RefreshResponse], error) {
	pair, err := h.svc.Refresh(ctx, req.Msg.GetRefreshToken())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.RefreshResponse{Tokens: toTokenPair(pair)}), nil
}

func (h *Handler) IssueScopedToken(ctx context.Context, req *connect.Request[identityv1.IssueScopedTokenRequest]) (*connect.Response[identityv1.IssueScopedTokenResponse], error) {
	m := req.Msg
	scope := scopeFromRef(m.GetScope())
	pair, err := h.svc.IssueScopedToken(ctx, m.GetUserId(), scope, m.GetRole())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.IssueScopedTokenResponse{Tokens: toTokenPair(pair)}), nil
}

func (h *Handler) CustomerSession(ctx context.Context, req *connect.Request[identityv1.CustomerSessionRequest]) (*connect.Response[identityv1.CustomerSessionResponse], error) {
	pair, err := h.svc.CustomerSession(ctx, req.Msg.GetRestaurantId(), req.Msg.GetTable())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.CustomerSessionResponse{Tokens: toTokenPair(pair)}), nil
}

func (h *Handler) Introspect(ctx context.Context, req *connect.Request[identityv1.IntrospectRequest]) (*connect.Response[identityv1.IntrospectResponse], error) {
	res, err := h.svc.Introspect(ctx, req.Msg.GetAccessToken())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&identityv1.IntrospectResponse{
		Active: res.Active,
		UserId: res.UserID,
		Role:   res.Role,
		Scope:  refFromScope(res.Scope),
	}), nil
}

// --- mappers ---

func toTokenPair(p app.TokenPair) *identityv1.TokenPair {
	return &identityv1.TokenPair{
		AccessToken:  p.AccessToken,
		RefreshToken: p.RefreshToken,
		ExpiresIn:    p.ExpiresIn,
	}
}

func toUserProto(u domain.User) *identityv1.User {
	return &identityv1.User{
		Id:          u.ID,
		Email:       u.Email,
		Phone:       u.Phone,
		DisplayName: u.DisplayName,
		Realm:       u.Realm,
		Active:      u.Active,
	}
}

func scopeFromRef(r *commonv1.TenantRef) domain.TenantScope {
	if r == nil {
		return domain.TenantScope{}
	}
	return domain.TenantScope{
		OwnerID:      r.GetOwnerId(),
		BrandID:      r.GetBrandId(),
		RestaurantID: r.GetRestaurantId(),
	}
}

func refFromScope(s domain.TenantScope) *commonv1.TenantRef {
	return &commonv1.TenantRef{
		OwnerId:      s.OwnerID,
		BrandId:      s.BrandID,
		RestaurantId: s.RestaurantID,
	}
}

// toConnect maps domain errors to Connect codes. Validation + lookup errors
// are translated to the canonical pkg/errors sentinels first, then ToConnect
// assigns the wire code; anything unrecognized falls through as Internal.
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrUserNotFound),
		errors.Is(err, domain.ErrChallengeNotFound):
		return pkgerrors.ToConnect(pkgerrors.ErrNotFound)
	case errors.Is(err, domain.ErrInvalidAddress),
		errors.Is(err, domain.ErrInvalidChannel),
		errors.Is(err, domain.ErrCodeMismatch):
		return pkgerrors.ToConnect(pkgerrors.ErrInvalid)
	case errors.Is(err, domain.ErrTooManyAttempts):
		return pkgerrors.ToConnect(pkgerrors.ErrQuotaExceeded)
	case errors.Is(err, domain.ErrChallengeExpired),
		errors.Is(err, domain.ErrChallengeConsumed),
		errors.Is(err, domain.ErrUserInactive):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return pkgerrors.ToConnect(err)
	}
}
