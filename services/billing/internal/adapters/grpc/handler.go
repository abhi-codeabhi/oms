// Package grpc is the Connect handler for BillingService. It maps proto requests
// to app use cases, app/domain types back to proto, and domain errors to Connect
// codes. The restaurant id ALWAYS comes from the JWT-derived tenancy scope, never
// the request body (CONVENTIONS.md). No business logic lives here (map only).
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	billingv1 "github.com/restorna/platform/gen/go/restorna/billing/v1"
	"github.com/restorna/platform/gen/go/restorna/billing/v1/billingv1connect"
	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/billing/internal/app"
	"github.com/restorna/platform/services/billing/internal/domain"
)

// Handler adapts *app.App to the generated BillingServiceHandler interface.
type Handler struct {
	billingv1connect.UnimplementedBillingServiceHandler
	uc *app.App
}

var _ billingv1connect.BillingServiceHandler = (*Handler)(nil)

// New builds a Connect handler around the use-case app.
func New(uc *app.App) *Handler { return &Handler{uc: uc} }

// OpenTabs returns the live billing board (event-driven read model).
func (h *Handler) OpenTabs(ctx context.Context, _ *connect.Request[billingv1.OpenTabsRequest]) (*connect.Response[billingv1.OpenTabsResponse], error) {
	tabs, err := h.uc.OpenTabs(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*billingv1.Tab, 0, len(tabs))
	for _, t := range tabs {
		out = append(out, tabToProto(t))
	}
	return connect.NewResponse(&billingv1.OpenTabsResponse{Tabs: out}), nil
}

// OpenForTable aggregates the table's unbilled orders into one categorized bill.
func (h *Handler) OpenForTable(ctx context.Context, req *connect.Request[billingv1.OpenForTableRequest]) (*connect.Response[billingv1.OpenForTableResponse], error) {
	res, err := h.uc.OpenForTable(ctx, restaurantFromCtx(ctx), req.Msg.GetTable())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&billingv1.OpenForTableResponse{
		Bill:       billToProto(res.Bill, res.Totals),
		Sections:   sectionsToProto(res.Sections),
		OrderCount: int32(res.OrderCount),
	}), nil
}

// GetBill returns a bill with its computed totals + sections.
func (h *Handler) GetBill(ctx context.Context, req *connect.Request[billingv1.GetBillRequest]) (*connect.Response[billingv1.GetBillResponse], error) {
	v, err := h.uc.GetBill(ctx, restaurantFromCtx(ctx), req.Msg.GetBillId())
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&billingv1.GetBillResponse{
		Bill:     billToProto(v.Bill, v.Totals),
		Sections: sectionsToProto(v.Sections),
	}), nil
}

// ListOpen returns the unpaid bills (the billing surface's queue).
func (h *Handler) ListOpen(ctx context.Context, _ *connect.Request[billingv1.ListOpenRequest]) (*connect.Response[billingv1.ListOpenResponse], error) {
	views, err := h.uc.ListOpen(ctx, restaurantFromCtx(ctx))
	if err != nil {
		return nil, toConnect(err)
	}
	bills := make([]*billingv1.Bill, 0, len(views))
	for _, v := range views {
		bills = append(bills, billToProto(v.Bill, v.Totals))
	}
	return connect.NewResponse(&billingv1.ListOpenResponse{Bills: bills}), nil
}

// ApplyDiscount lowers the bill total (coupon or flat amount) and recomputes.
func (h *Handler) ApplyDiscount(ctx context.Context, req *connect.Request[billingv1.ApplyDiscountRequest]) (*connect.Response[billingv1.ApplyDiscountResponse], error) {
	in := app.ApplyDiscountInput{
		BillID: req.Msg.GetBillId(),
		Reason: req.Msg.GetReason(),
	}
	if amt := req.Msg.GetAmount(); amt != nil {
		in.AmountMinor = amt.GetMinor()
	}
	// A coupon code may be passed through the reason field convention "coupon:CODE"
	// so the flat-amount RPC shape can also trigger a promotions evaluation.
	if code := couponFromReason(req.Msg.GetReason()); code != "" {
		in.CouponCode = code
	}
	v, err := h.uc.ApplyDiscount(ctx, restaurantFromCtx(ctx), in)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&billingv1.ApplyDiscountResponse{Bill: billToProto(v.Bill, v.Totals)}), nil
}

// TakePayment records a payment; finalizes the bill when paid in full.
func (h *Handler) TakePayment(ctx context.Context, req *connect.Request[billingv1.TakePaymentRequest]) (*connect.Response[billingv1.TakePaymentResponse], error) {
	var amount int64
	if amt := req.Msg.GetAmount(); amt != nil {
		amount = amt.GetMinor()
	}
	res, err := h.uc.TakePayment(ctx, restaurantFromCtx(ctx), req.Msg.GetBillId(), req.Msg.GetMethod(), amount, "")
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&billingv1.TakePaymentResponse{
		Bill: billToProto(res.Bill, res.Totals),
		Paid: res.Paid,
	}), nil
}

// --- mapping helpers ---

const rfc3339 = "2006-01-02T15:04:05Z07:00"

func mn(m money.Money) *commonv1.Money {
	return &commonv1.Money{Minor: m.Minor, Currency: m.Currency}
}

func billToProto(b domain.Bill, t domain.Totals) *billingv1.Bill {
	lines := make([]*billingv1.BillLine, 0, len(b.Lines))
	for _, l := range b.Lines {
		lines = append(lines, &billingv1.BillLine{
			Id:       l.ID,
			Name:     l.Name,
			Category: l.Category,
			Price:    mn(l.Price),
		})
	}
	pays := make([]*billingv1.Payment, 0, len(b.Payments))
	for _, p := range b.Payments {
		pays = append(pays, &billingv1.Payment{
			Id:     p.ID,
			Method: p.Method,
			Amount: mn(p.Amount),
			Ref:    p.Ref,
			At:     p.At.UTC().Format(rfc3339),
		})
	}
	return &billingv1.Bill{
		Id:        b.ID,
		Table:     b.Table,
		OrderIds:  b.OrderIDs,
		Lines:     lines,
		Subtotal:  mn(t.Subtotal),
		Tax:       mn(t.Tax),
		Discount:  mn(t.Discount),
		Total:     mn(t.Total),
		Payments:  pays,
		Paid:      b.Paid,
		CreatedAt: b.CreatedAt.UTC().Format(rfc3339),
	}
}

func sectionsToProto(in []domain.Section) []*billingv1.Section {
	out := make([]*billingv1.Section, 0, len(in))
	for _, s := range in {
		out = append(out, &billingv1.Section{
			Category: s.Category,
			Count:    s.Count,
			Subtotal: mn(s.Subtotal),
		})
	}
	return out
}

func tabToProto(t domain.Tab) *billingv1.Tab {
	return &billingv1.Tab{
		Table:      t.Table,
		OrderCount: t.OrderCount,
		ItemCount:  t.ItemCount,
		Running:    mn(t.Running),
		Asked:      t.Asked,
		BillId:     t.BillID,
		BillTotal:  mn(t.BillTotal),
		Status:     t.Status(),
	}
}

// couponFromReason extracts a coupon code passed as "coupon:CODE" in the reason
// field (a convenience until the proto carries a dedicated coupon field).
func couponFromReason(reason string) string {
	const prefix = "coupon:"
	if len(reason) > len(prefix) && reason[:len(prefix)] == prefix {
		return reason[len(prefix):]
	}
	return ""
}

// toConnect maps domain/app errors to Connect codes (the ONLY place this happens).
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound), errors.Is(err, pkgerrors.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case errors.Is(err, domain.ErrInvalid), errors.Is(err, pkgerrors.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case errors.Is(err, pkgerrors.ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// restaurantFromCtx reads the JWT-derived tenancy scope set by the auth
// interceptor. The restaurant id (the billing tenant key) ALWAYS comes from the
// auth context, never the request body (CONVENTIONS.md multi-tenancy rule).
func restaurantFromCtx(ctx context.Context) string {
	if s, ok := tenancy.From(ctx); ok {
		return s.RestaurantID
	}
	return ""
}
