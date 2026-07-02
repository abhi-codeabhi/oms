package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
)

// phonepeBaseURL is the PhonePe PG (hermes) root; overridable in tests.
const phonepeBaseURL = "https://api.phonepe.com/apis/hermes"

// PhonePe implements connector.PaymentConnector against PhonePe's PG API. Each
// request carries an X-VERIFY header: sha256(base64Payload + apiPath + saltKey)
// followed by "###" + saltIndex. Webhook callbacks are verified with the same
// scheme over the raw (base64) response body.
//
// Config keys: merchant_id, salt_key, salt_index.
type PhonePe struct {
	merchantID string
	saltKey    string
	saltIndex  string
	baseURL    string
	client     httpDoer
}

func NewPhonePe() *PhonePe {
	return &PhonePe{baseURL: phonepeBaseURL, saltIndex: "1", client: newHTTPClient()}
}

func (p *PhonePe) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "phonepe",
		Name:         "PhonePe",
		Capabilities: []connector.Capability{connector.CapabilityPayment},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["merchant_id", "salt_key", "salt_index"],
  "properties": {
    "merchant_id": {"type": "string", "title": "Merchant ID"},
    "salt_key":    {"type": "string", "title": "Salt Key", "secret": true},
    "salt_index":  {"type": "string", "title": "Salt Index", "default": "1"}
  }
}`),
	}
}

func (p *PhonePe) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "merchant_id", "salt_key", "salt_index"); err != nil {
		return err
	}
	p.merchantID = cfgGet(cfg, "merchant_id")
	p.saltKey = cfgGet(cfg, "salt_key")
	p.saltIndex = cfgGet(cfg, "salt_index")
	if url := cfgGet(cfg, "base_url"); url != "" {
		p.baseURL = url
	}
	if p.baseURL == "" {
		p.baseURL = phonepeBaseURL
	}
	if p.client == nil {
		p.client = newHTTPClient()
	}
	return nil
}

// xVerify computes the PhonePe X-VERIFY value: sha256(payload + path + saltKey)###saltIndex.
func (p *PhonePe) xVerify(payloadPlusPath string) string {
	sum := sha256.Sum256([]byte(payloadPlusPath + p.saltKey))
	return fmt.Sprintf("%x###%s", sum[:], p.saltIndex)
}

// CreateIntent calls /pg/v1/pay with a base64-wrapped request and returns the
// merchantTransactionId as the provider reference.
func (p *PhonePe) CreateIntent(ctx context.Context, amount money.Money, ref string) (string, error) {
	const path = "/pg/v1/pay"
	inner := map[string]any{
		"merchantId":            p.merchantID,
		"merchantTransactionId": ref,
		"amount":                amount.Minor,
		"paymentInstrument":     map[string]any{"type": "PAY_PAGE"},
	}
	innerJSON, _ := json.Marshal(inner)
	b64 := base64.StdEncoding.EncodeToString(innerJSON)

	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+path, map[string]string{"request": b64})
	if err != nil {
		return "", err
	}
	req.Header.Set("X-VERIFY", p.xVerify(b64+path))
	var out struct {
		Success bool `json:"success"`
	}
	if err := doJSON(p.client, req, &out); err != nil {
		return "", err
	}
	if !out.Success {
		return "", fmt.Errorf("phonepe: pay not successful")
	}
	return ref, nil
}

// Capture queries the transaction status (PhonePe collect flows auto-capture) and
// returns the raw status response as the receipt.
func (p *PhonePe) Capture(ctx context.Context, provRef string) (json.RawMessage, error) {
	path := fmt.Sprintf("/pg/v1/status/%s/%s", p.merchantID, provRef)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-VERIFY", p.xVerify(path))
	req.Header.Set("X-MERCHANT-ID", p.merchantID)
	var receipt json.RawMessage
	if err := doJSON(p.client, req, &receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Refund initiates a refund via /pg/v1/refund.
func (p *PhonePe) Refund(ctx context.Context, provRef string, amount money.Money) error {
	const path = "/pg/v1/refund"
	inner := map[string]any{
		"merchantId":                 p.merchantID,
		"originalTransactionId":      provRef,
		"merchantTransactionId":      provRef + "-rf",
		"amount":                     amount.Minor,
	}
	innerJSON, _ := json.Marshal(inner)
	b64 := base64.StdEncoding.EncodeToString(innerJSON)
	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+path, map[string]string{"request": b64})
	if err != nil {
		return err
	}
	req.Header.Set("X-VERIFY", p.xVerify(b64+path))
	return doJSON(p.client, req, nil)
}

// VerifyWebhook verifies the X-VERIFY over the base64 response body. The sig passed
// in is the X-VERIFY header value the hub extracted. PhonePe callbacks wrap the
// base64 payload in {"response": "<b64>"}.
func (p *PhonePe) VerifyWebhook(_ context.Context, body []byte, sig string) (events.Event, error) {
	var wrap struct {
		Response string `json:"response"`
	}
	if err := json.Unmarshal(body, &wrap); err != nil {
		return events.Event{}, fmt.Errorf("phonepe: decode webhook: %w", err)
	}
	// X-VERIFY for callbacks is sha256(base64Body + saltKey)###saltIndex (no path).
	sum := sha256.Sum256([]byte(wrap.Response + p.saltKey))
	expected := fmt.Sprintf("%x###%s", sum[:], p.saltIndex)
	if !phonepeSigEqual(sig, expected) {
		return events.Event{}, fmt.Errorf("phonepe: X-VERIFY mismatch")
	}
	decoded, err := base64.StdEncoding.DecodeString(wrap.Response)
	if err != nil {
		return events.Event{}, fmt.Errorf("phonepe: decode payload: %w", err)
	}
	var payload struct {
		Code string `json:"code"`
		Data struct {
			MerchantTransactionID string `json:"merchantTransactionId"`
			State                 string `json:"state"`
		} `json:"data"`
	}
	_ = json.Unmarshal(decoded, &payload)
	typ := EventPaymentCaptured
	if payload.Code != "PAYMENT_SUCCESS" && payload.Data.State != "COMPLETED" {
		typ = EventPaymentFailed
	}
	return events.New(typ, "", map[string]any{
		"connector_id": "phonepe",
		"provider_ref": payload.Data.MerchantTransactionID,
		"status":       payload.Data.State,
		"code":         payload.Code,
	}), nil
}

// phonepeSigEqual compares X-VERIFY values, ignoring the "###index" suffix for the
// hash portion so an index formatting difference doesn't reject a valid sig.
func phonepeSigEqual(got, want string) bool {
	gh := strings.SplitN(got, "###", 2)[0]
	wh := strings.SplitN(want, "###", 2)[0]
	return constantTimeEqualHex(gh, wh)
}

var _ connector.PaymentConnector = (*PhonePe)(nil)
