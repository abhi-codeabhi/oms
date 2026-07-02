// Package clients: BillingService -> ports.BillingOpen. A table with an open
// (unpaid) bill DERIVES to "billing" on the floor view.
package clients

import (
	"context"
	"net/http"

	"connectrpc.com/connect"

	billingv1 "github.com/restorna/platform/gen/go/restorna/billing/v1"
	"github.com/restorna/platform/gen/go/restorna/billing/v1/billingv1connect"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// BillingClient implements ports.BillingOpen over a Connect BillingService client.
type BillingClient struct {
	svc billingv1connect.BillingServiceClient
}

var _ ports.BillingOpen = (*BillingClient)(nil)

// NewBilling builds a BillingClient talking to the billing service at baseURL.
func NewBilling(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *BillingClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &BillingClient{svc: billingv1connect.NewBillingServiceClient(httpClient, baseURL, opts...)}
}

// NewBillingFromClient wraps an already-built generated client (tests/wiring).
func NewBillingFromClient(svc billingv1connect.BillingServiceClient) *BillingClient {
	return &BillingClient{svc: svc}
}

// ListOpen returns the open bills' table labels (BillingService.ListOpen).
func (c *BillingClient) ListOpen(ctx context.Context, _ string) ([]ports.OpenBill, error) {
	resp, err := c.svc.ListOpen(ctx, connect.NewRequest(&billingv1.ListOpenRequest{}))
	if err != nil {
		return nil, err
	}
	bills := resp.Msg.GetBills()
	out := make([]ports.OpenBill, 0, len(bills))
	for _, b := range bills {
		// A bill in ListOpen is unpaid by definition; guard with Paid just in case.
		if b.GetPaid() {
			continue
		}
		out = append(out, ports.OpenBill{Table: b.GetTable()})
	}
	return out, nil
}
