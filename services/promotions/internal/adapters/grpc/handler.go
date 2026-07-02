// Package grpc is the Connect handler for PromotionsService. It maps proto requests
// to app use cases, app/domain types back to proto, and domain errors to Connect
// codes. No business logic lives here (CONVENTIONS.md: map only). The trusted tenant
// (restaurant_id) ALWAYS comes from the auth context, never the request body.
package grpc

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	promotionsv1 "github.com/restorna/platform/gen/go/restorna/promotions/v1"
	"github.com/restorna/platform/gen/go/restorna/promotions/v1/promotionsv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/promotions/internal/app"
	"github.com/restorna/platform/services/promotions/internal/domain"
)

// Handler adapts *app.App to the generated PromotionsServiceHandler interface.
type Handler struct {
	promotionsv1connect.UnimplementedPromotionsServiceHandler
	uc *app.App
}

var _ promotionsv1connect.PromotionsServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// UpsertCoupon creates or replaces a coupon for the caller's restaurant.
func (h *Handler) UpsertCoupon(ctx context.Context, req *connect.Request[promotionsv1.UpsertCouponRequest]) (*connect.Response[promotionsv1.UpsertCouponResponse], error) {
	c := req.Msg.GetCoupon()
	out, err := h.uc.UpsertCoupon(ctx, restaurantFromCtx(ctx), domain.NewCouponInput{
		Code:     c.GetCode(),
		Type:     c.GetType(),
		Value:    c.GetValue(),
		MinOrder: moneyFromProto(c.GetMinOrder()),
		Category: c.GetCategory(),
		Active:   c.GetActive(),
		StartsAt: c.GetStartsAt(),
		EndsAt:   c.GetEndsAt(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&promotionsv1.UpsertCouponResponse{Coupon: couponToProto(out)}), nil
}

// ListCoupons returns every coupon configured for the caller's restaurant.
func (h *Handler) ListCoupons(ctx context.Context, _ *connect.Request[promotionsv1.ListCouponsRequest]) (*connect.Response[promotionsv1.ListCouponsResponse], error) {
	coupons, err := h.uc.ListCoupons(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*promotionsv1.Coupon, 0, len(coupons))
	for _, c := range coupons {
		out = append(out, couponToProto(c))
	}
	return connect.NewResponse(&promotionsv1.ListCouponsResponse{Coupons: out}), nil
}

// ToggleCoupon flips a coupon active/inactive by code.
func (h *Handler) ToggleCoupon(ctx context.Context, req *connect.Request[promotionsv1.ToggleCouponRequest]) (*connect.Response[promotionsv1.ToggleCouponResponse], error) {
	out, err := h.uc.ToggleCoupon(ctx, restaurantFromCtx(ctx), req.Msg.GetCode(), req.Msg.GetActive())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&promotionsv1.ToggleCouponResponse{Coupon: couponToProto(out)}), nil
}

// Evaluate returns the best discount for a cart context plus the coupon that applied.
func (h *Handler) Evaluate(ctx context.Context, req *connect.Request[promotionsv1.EvaluateRequest]) (*connect.Response[promotionsv1.EvaluateResponse], error) {
	ev, err := h.uc.Evaluate(ctx, restaurantFromCtx(ctx), app.EvaluateInput{
		Subtotal:   moneyFromProto(req.Msg.GetSubtotal()),
		CouponCode: req.Msg.GetCouponCode(),
		Category:   req.Msg.GetCategory(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&promotionsv1.EvaluateResponse{
		Discount: moneyToProto(ev.Discount),
		Applied:  ev.Applied,
	}), nil
}

// --- mapping helpers ---

func couponToProto(c domain.Coupon) *promotionsv1.Coupon {
	return &promotionsv1.Coupon{
		Code:     c.Code,
		Type:     c.Type,
		Value:    c.Value,
		MinOrder: moneyToProto(c.MinOrder),
		Category: c.Category,
		Active:   c.Active,
		StartsAt: timeToProto(c.StartsAt),
		EndsAt:   timeToProto(c.EndsAt),
	}
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

func moneyToProto(m money.Money) *commonv1.Money {
	return &commonv1.Money{Minor: m.Minor, Currency: m.Currency}
}

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func timeToProto(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(rfc3339)
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, tenancy.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
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

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth interceptor.
// The restaurant id ALWAYS comes from the auth context, never the request body
// (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
