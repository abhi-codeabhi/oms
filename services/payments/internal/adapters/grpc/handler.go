// Package grpc is the Connect handler for PaymentsService. It maps proto requests
// to app use cases, app/domain types back to proto, and domain errors to Connect
// codes. No business logic lives here (CONVENTIONS.md: map only).
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	paymentsv1 "github.com/restorna/platform/gen/go/restorna/payments/v1"
	"github.com/restorna/platform/gen/go/restorna/payments/v1/paymentsv1connect"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/payments/internal/app"
	"github.com/restorna/platform/services/payments/internal/domain"
)

// Handler adapts *app.App to the generated PaymentsServiceHandler interface.
type Handler struct {
	paymentsv1connect.UnimplementedPaymentsServiceHandler
	uc *app.App
}

var _ paymentsv1connect.PaymentsServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// CreateIntent mints an idempotent payment intent and returns the provider handoff.
func (h *Handler) CreateIntent(ctx context.Context, req *connect.Request[paymentsv1.CreateIntentRequest]) (*connect.Response[paymentsv1.CreateIntentResponse], error) {
	res, err := h.uc.CreateIntent(ctx, app.CreateIntentInput{
		RestaurantID:      restaurantFromCtx(ctx),
		BillID:            req.Msg.GetBillId(),
		Amount:            moneyFromProto(req.Msg.GetAmount()),
		PreferConnectorID: req.Msg.GetPreferConnectorId(),
		IdempotencyKey:    req.Msg.GetIdempotencyKey(),
		CustomerContact:   req.Msg.GetCustomerContact(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&paymentsv1.CreateIntentResponse{
		Payment: paymentToProto(res.Payment),
		Handoff: res.Handoff,
	}), nil
}

// Capture confirms an authorized intent.
func (h *Handler) Capture(ctx context.Context, req *connect.Request[paymentsv1.CaptureRequest]) (*connect.Response[paymentsv1.CaptureResponse], error) {
	p, err := h.uc.Capture(ctx, restaurantFromCtx(ctx), req.Msg.GetPaymentId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&paymentsv1.CaptureResponse{Payment: paymentToProto(p)}), nil
}

// Refund issues a full/partial refund.
func (h *Handler) Refund(ctx context.Context, req *connect.Request[paymentsv1.RefundRequest]) (*connect.Response[paymentsv1.RefundResponse], error) {
	p, err := h.uc.Refund(ctx, restaurantFromCtx(ctx), req.Msg.GetPaymentId(), moneyFromProto(req.Msg.GetAmount()), req.Msg.GetReason())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&paymentsv1.RefundResponse{Payment: paymentToProto(p)}), nil
}

// GetPayment fetches a payment by id (RLS-scoped).
func (h *Handler) GetPayment(ctx context.Context, req *connect.Request[paymentsv1.GetPaymentRequest]) (*connect.Response[paymentsv1.GetPaymentResponse], error) {
	p, err := h.uc.GetPayment(ctx, restaurantFromCtx(ctx), req.Msg.GetPaymentId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&paymentsv1.GetPaymentResponse{Payment: paymentToProto(p)}), nil
}

// --- mapping helpers ---

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func paymentToProto(p domain.Payment) *paymentsv1.Payment {
	return &paymentsv1.Payment{
		Id:          p.ID,
		BillId:      p.BillID,
		Amount:      &commonv1.Money{Minor: p.Amount.Minor, Currency: p.Amount.Currency},
		ConnectorId: p.ConnectorID,
		ProviderRef: p.ProviderRef,
		Status:      statusToProto(p.Status),
		Method:      p.Method,
		CreatedAt:   p.CreatedAt.UTC().Format(rfc3339),
	}
}

func statusToProto(s domain.Status) paymentsv1.Status {
	switch s {
	case domain.StatusCreated:
		return paymentsv1.Status_CREATED
	case domain.StatusPending:
		return paymentsv1.Status_PENDING
	case domain.StatusCaptured:
		return paymentsv1.Status_CAPTURED
	case domain.StatusFailed:
		return paymentsv1.Status_FAILED
	case domain.StatusRefunded:
		return paymentsv1.Status_REFUNDED
	default:
		return paymentsv1.Status_STATUS_UNSPECIFIED
	}
}

func moneyFromProto(m *commonv1.Money) money.Money {
	if m == nil {
		return money.Money{}
	}
	return money.New(m.GetMinor(), m.GetCurrency())
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, domain.ErrInvalidState),
		errors.Is(err, domain.ErrRefundExceeds),
		errors.Is(err, domain.ErrCurrencyMismatch),
		errors.Is(err, domain.ErrAmountMismatch):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth
// interceptor. The tenant id ALWAYS comes from the auth context, never the request
// body (CONVENTIONS.md multi-tenancy rule). Payments are per-outlet.
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
