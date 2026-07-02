package connectors

import (
	"context"
	"fmt"
	"sort"

	"github.com/restorna/platform/pkg/connector"
)

// Normalized webhook event types the payment connectors emit from VerifyWebhook
// (and that connector-hub re-publishes for the payments service to consume). Kept
// here so payments and the connectors share one vocabulary.
const (
	EventPaymentCaptured = "restorna.connector.payment.captured"
	EventPaymentFailed   = "restorna.connector.payment.failed"
	EventPaymentRefunded = "restorna.connector.payment.refunded"
)

// paymentBuilder constructs an uninitialized payment connector; NewPayment Inits it.
type paymentBuilder func() connector.PaymentConnector

// paymentBuilders maps a connector id (matching connector-hub Manifest.ID /
// ResolveResponse.connector_id) to its adapter constructor. Adding a gateway is
// additive: implement connector.PaymentConnector in its own file, register it here.
var paymentBuilders = map[string]paymentBuilder{
	"razorpay": func() connector.PaymentConnector { return NewRazorpay() },
	"paytm":    func() connector.PaymentConnector { return NewPaytm() },
	"phonepe":  func() connector.PaymentConnector { return NewPhonePe() },
	"mockpay":  func() connector.PaymentConnector { return NewMockPay() },
	"mock":     func() connector.PaymentConnector { return NewMockPay() }, // alias for tests/dev
}

// NewPayment instantiates the payment connector registered under id and initializes
// it with cfg (decrypted per-tenant config from connector-hub's Resolve). It is a
// typed convenience over the unified New (registry.go) for the payments service,
// which only ever wants a PaymentConnector. It returns an error for an unknown id
// (surface as FailedPrecondition upstream) or when Init rejects the config.
func NewPayment(id string, cfg map[string]string) (connector.PaymentConnector, error) {
	b, ok := paymentBuilders[id]
	if !ok {
		return nil, fmt.Errorf("connectors: unknown payment connector %q", id)
	}
	c := b()
	if err := c.Init(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("connectors: init %q: %w", id, err)
	}
	return c, nil
}

// RegisterPayment adds/overrides a payment builder (used by plugins or tests).
func RegisterPayment(id string, b func() connector.PaymentConnector) { paymentBuilders[id] = b }

// PaymentIDs lists the registered payment connector ids (sorted; diagnostics).
func PaymentIDs() []string {
	ids := make([]string, 0, len(paymentBuilders))
	for id := range paymentBuilders {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
