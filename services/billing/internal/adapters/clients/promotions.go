package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	promotionsv1 "github.com/restorna/platform/gen/go/restorna/promotions/v1"
	"github.com/restorna/platform/gen/go/restorna/promotions/v1/promotionsv1connect"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Promotions implements ports.Promotions over a Connect PromotionsService client.
// It evaluates a coupon against the bill subtotal to produce a discount amount.
type Promotions struct {
	svc promotionsv1connect.PromotionsServiceClient
}

var _ ports.Promotions = (*Promotions)(nil)

// NewPromotions builds a Promotions client talking to baseURL (e.g. "http://promotions:8080").
func NewPromotions(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Promotions {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Promotions{svc: promotionsv1connect.NewPromotionsServiceClient(httpClient, baseURL, opts...)}
}

// NewPromotionsFromClient wraps an already-built generated client (tests/wiring).
func NewPromotionsFromClient(svc promotionsv1connect.PromotionsServiceClient) *Promotions {
	return &Promotions{svc: svc}
}

// Evaluate returns the discount minor units for a coupon code against a subtotal
// (0 when the coupon does not apply). applied describes what matched.
func (p *Promotions) Evaluate(ctx context.Context, _ string, couponCode string, subtotal money.Money) (int64, string, error) {
	resp, err := p.svc.Evaluate(ctx, connect.NewRequest(&promotionsv1.EvaluateRequest{
		Subtotal:   &commonv1.Money{Minor: subtotal.Minor, Currency: subtotal.Currency},
		CouponCode: couponCode,
	}))
	if err != nil {
		return 0, "", err
	}
	var minor int64
	if d := resp.Msg.GetDiscount(); d != nil {
		minor = d.GetMinor()
	}
	return minor, resp.Msg.GetApplied(), nil
}
