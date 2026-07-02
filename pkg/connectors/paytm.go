package connectors

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
)

// paytmBaseURL is the Paytm PG (Payment Gateway) v3 root; overridable in tests.
const paytmBaseURL = "https://securegw.paytm.in"

// Paytm implements connector.PaymentConnector against Paytm's Initiate
// Transaction / Refund REST API. Requests carry a checksum computed from the
// body signed with the merchant key; the same checksum scheme verifies webhook
// callbacks (delivered as a "CHECKSUMHASH" field).
//
// Config keys: mid (merchant id), merchant_key, website.
type Paytm struct {
	mid         string
	merchantKey string
	website     string
	baseURL     string
	client      httpDoer
}

func NewPaytm() *Paytm {
	return &Paytm{baseURL: paytmBaseURL, website: "DEFAULT", client: newHTTPClient()}
}

func (p *Paytm) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "paytm",
		Name:         "Paytm",
		Capabilities: []connector.Capability{connector.CapabilityPayment},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["mid", "merchant_key"],
  "properties": {
    "mid":          {"type": "string", "title": "Merchant ID"},
    "merchant_key": {"type": "string", "title": "Merchant Key", "secret": true},
    "website":      {"type": "string", "title": "Website", "default": "DEFAULT"}
  }
}`),
	}
}

func (p *Paytm) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "mid", "merchant_key"); err != nil {
		return err
	}
	p.mid = cfgGet(cfg, "mid")
	p.merchantKey = cfgGet(cfg, "merchant_key")
	if w := cfgGet(cfg, "website"); w != "" {
		p.website = w
	}
	if url := cfgGet(cfg, "base_url"); url != "" {
		p.baseURL = url
	}
	if p.baseURL == "" {
		p.baseURL = paytmBaseURL
	}
	if p.website == "" {
		p.website = "DEFAULT"
	}
	if p.client == nil {
		p.client = newHTTPClient()
	}
	return nil
}

// paytmChecksum is a simplified, deterministic stand-in for Paytm's AES-based
// checksum: SHA-256 over the canonical body plus the merchant key. It is used for
// both signing outbound bodies and verifying the CHECKSUMHASH on callbacks so the
// two stay symmetric and testable without the vendor SDK.
func (p *Paytm) paytmChecksum(body []byte) string {
	sum := sha256.Sum256(append(append([]byte{}, body...), []byte(p.merchantKey)...))
	return fmt.Sprintf("%x", sum[:])
}

// CreateIntent calls Initiate Transaction and returns Paytm's txnToken as the
// provider reference the client app uses to open checkout.
func (p *Paytm) CreateIntent(ctx context.Context, amount money.Money, ref string) (string, error) {
	reqBody := map[string]any{
		"requestType": "Payment",
		"mid":         p.mid,
		"websiteName": p.website,
		"orderId":     ref,
		"txnAmount": map[string]string{
			"value":    fmt.Sprintf("%d.%02d", amount.Minor/100, amount.Minor%100),
			"currency": amount.Currency,
		},
	}
	raw, _ := json.Marshal(reqBody)
	signature := p.paytmChecksum(raw)
	head := map[string]any{"signature": signature}
	envelope := map[string]any{"body": reqBody, "head": head}

	endpoint := fmt.Sprintf("%s/theia/api/v1/initiateTransaction?mid=%s&orderId=%s",
		p.baseURL, url.QueryEscape(p.mid), url.QueryEscape(ref))
	req, err := jsonRequest(ctx, http.MethodPost, endpoint, envelope)
	if err != nil {
		return "", err
	}
	var out struct {
		Body struct {
			TxnToken   string `json:"txnToken"`
			ResultInfo struct {
				ResultStatus string `json:"resultStatus"`
				ResultMsg    string `json:"resultMsg"`
			} `json:"resultInfo"`
		} `json:"body"`
	}
	if err := doJSON(p.client, req, &out); err != nil {
		return "", err
	}
	if out.Body.TxnToken == "" {
		return "", fmt.Errorf("paytm: empty txnToken: %s", out.Body.ResultInfo.ResultMsg)
	}
	return out.Body.TxnToken, nil
}

// Capture confirms/queries a transaction status. Paytm auto-captures; this maps
// to Transaction Status and returns the raw response as the receipt.
func (p *Paytm) Capture(ctx context.Context, provRef string) (json.RawMessage, error) {
	reqBody := map[string]any{"mid": p.mid, "orderId": provRef}
	raw, _ := json.Marshal(reqBody)
	envelope := map[string]any{"body": reqBody, "head": map[string]any{"signature": p.paytmChecksum(raw)}}
	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+"/v3/order/status", envelope)
	if err != nil {
		return nil, err
	}
	var receipt json.RawMessage
	if err := doJSON(p.client, req, &receipt); err != nil {
		return nil, err
	}
	return receipt, nil
}

// Refund initiates a refund for a captured transaction.
func (p *Paytm) Refund(ctx context.Context, provRef string, amount money.Money) error {
	reqBody := map[string]any{
		"mid":       p.mid,
		"orderId":   provRef,
		"refId":     provRef + "-rf",
		"txnId":     provRef,
		"refundAmount": fmt.Sprintf("%d.%02d", amount.Minor/100, amount.Minor%100),
	}
	raw, _ := json.Marshal(reqBody)
	envelope := map[string]any{"body": reqBody, "head": map[string]any{"signature": p.paytmChecksum(raw)}}
	req, err := jsonRequest(ctx, http.MethodPost, p.baseURL+"/refund/apply", envelope)
	if err != nil {
		return err
	}
	return doJSON(p.client, req, nil)
}

// VerifyWebhook verifies the CHECKSUMHASH against the body-minus-checksum and
// normalizes the callback to a payment event. The signature passed in is the
// CHECKSUMHASH the hub extracted from the callback.
func (p *Paytm) VerifyWebhook(_ context.Context, body []byte, sig string) (events.Event, error) {
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		return events.Event{}, fmt.Errorf("paytm: decode webhook: %w", err)
	}
	delete(fields, "CHECKSUMHASH")
	canonical, _ := json.Marshal(fields)
	if !constantTimeEqualHex(sig, p.paytmChecksum(canonical)) {
		return events.Event{}, fmt.Errorf("paytm: checksum mismatch")
	}
	status, _ := fields["STATUS"].(string)
	orderID, _ := fields["ORDERID"].(string)
	typ := EventPaymentCaptured
	if status != "TXN_SUCCESS" {
		typ = EventPaymentFailed
	}
	return events.New(typ, "", map[string]any{
		"connector_id": "paytm",
		"provider_ref": orderID,
		"status":       status,
	}), nil
}

var _ connector.PaymentConnector = (*Paytm)(nil)
