package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
)

// razorpayBaseURL is the Razorpay REST v1 root; overridable in tests.
const razorpayBaseURL = "https://api.razorpay.com/v1"

// Razorpay implements connector.PaymentConnector against Razorpay's Orders/
// Payments REST API. Auth is HTTP Basic (key_id:key_secret). Webhooks are signed
// with HMAC-SHA256 over the raw body using the dashboard webhook secret and
// delivered in the X-Razorpay-Signature header.
//
// Config keys: key_id, key_secret, webhook_secret.
type Razorpay struct {
	keyID         string
	keySecret     string
	webhookSecret string
	baseURL       string
	client        httpDoer
}

// NewRazorpay constructs an uninitialized adapter; call Init or use the factory.
func NewRazorpay() *Razorpay {
	return &Razorpay{baseURL: razorpayBaseURL, client: newHTTPClient()}
}

func (r *Razorpay) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "razorpay",
		Name:         "Razorpay",
		Capabilities: []connector.Capability{connector.CapabilityPayment},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["key_id", "key_secret", "webhook_secret"],
  "properties": {
    "key_id":         {"type": "string", "title": "Key ID"},
    "key_secret":     {"type": "string", "title": "Key Secret", "secret": true},
    "webhook_secret": {"type": "string", "title": "Webhook Secret", "secret": true}
  }
}`),
	}
}

func (r *Razorpay) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "key_id", "key_secret", "webhook_secret"); err != nil {
		return err
	}
	r.keyID = cfgGet(cfg, "key_id")
	r.keySecret = cfgGet(cfg, "key_secret")
	r.webhookSecret = cfgGet(cfg, "webhook_secret")
	if url := cfgGet(cfg, "base_url"); url != "" {
		r.baseURL = url
	}
	if r.baseURL == "" {
		r.baseURL = razorpayBaseURL
	}
	if r.client == nil {
		r.client = newHTTPClient()
	}
	return nil
}

// CreateIntent creates a Razorpay Order (amount in minor units) and returns its id.
func (r *Razorpay) CreateIntent(ctx context.Context, amount money.Money, ref string) (string, error) {
	req, err := jsonRequest(ctx, http.MethodPost, r.baseURL+"/orders", map[string]any{
		"amount":   amount.Minor,
		"currency": amount.Currency,
		"receipt":  ref,
	})
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(r.keyID, r.keySecret)
	var out struct {
		ID string `json:"id"`
	}
	if err := doJSON(r.client, req, &out); err != nil {
		return "", err
	}
	if out.ID == "" {
		return "", fmt.Errorf("razorpay: create order returned empty id")
	}
	return out.ID, nil
}

// Capture captures the given payment id. Razorpay requires the amount+currency on
// capture; for auto-capture flows this confirms the charge.
func (r *Razorpay) Capture(ctx context.Context, provRef string) (json.RawMessage, error) {
	req, err := jsonRequest(ctx, http.MethodPost, fmt.Sprintf("%s/payments/%s/capture", r.baseURL, provRef), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(r.keyID, r.keySecret)
	var receipt json.RawMessage
	if err := doJSON(r.client, req, &receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Refund issues a (partial) refund on a captured payment.
func (r *Razorpay) Refund(ctx context.Context, provRef string, amount money.Money) error {
	req, err := jsonRequest(ctx, http.MethodPost, fmt.Sprintf("%s/payments/%s/refund", r.baseURL, provRef), map[string]any{
		"amount": amount.Minor,
	})
	if err != nil {
		return err
	}
	req.SetBasicAuth(r.keyID, r.keySecret)
	return doJSON(r.client, req, nil)
}

// VerifyWebhook checks the X-Razorpay-Signature HMAC-SHA256 over the raw body and,
// on success, normalizes the event to a CloudEvent. This is the (body, sig) form
// required by connector.PaymentConnector.
func (r *Razorpay) VerifyWebhook(_ context.Context, body []byte, sig string) (events.Event, error) {
	expected := hmacSHA256Hex(r.webhookSecret, body)
	if !constantTimeEqualHex(sig, expected) {
		return events.Event{}, fmt.Errorf("razorpay: webhook signature mismatch")
	}
	return razorpayNormalize(body)
}

// razorpayNormalize maps a verified Razorpay webhook to a restorna payment event.
func razorpayNormalize(body []byte) (events.Event, error) {
	var payload struct {
		Event   string `json:"event"`
		Payload struct {
			Payment struct {
				Entity struct {
					ID       string `json:"id"`
					OrderID  string `json:"order_id"`
					Status   string `json:"status"`
					Amount   int64  `json:"amount"`
					Currency string `json:"currency"`
				} `json:"entity"`
			} `json:"payment"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return events.Event{}, fmt.Errorf("razorpay: decode webhook: %w", err)
	}
	typ := EventPaymentCaptured
	if payload.Event == "payment.failed" {
		typ = EventPaymentFailed
	}
	ent := payload.Payload.Payment.Entity
	return events.New(typ, "", map[string]any{
		"connector_id": "razorpay",
		"provider_ref": ent.OrderID,
		"payment_ref":  ent.ID,
		"status":       ent.Status,
		"amount_minor": ent.Amount,
		"currency":     ent.Currency,
	}), nil
}

var _ connector.PaymentConnector = (*Razorpay)(nil)
