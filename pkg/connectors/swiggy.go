package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
)

// swiggyBaseURL is the Swiggy partner API root; overridable in tests/config.
const swiggyBaseURL = "https://partner.swiggy.com/v1"

// Swiggy implements connector.AggregatorConnector against Swiggy's partner API.
// It mirrors Zomato: menu push is a JSON POST of the normalized catalog; inbound
// order webhooks are HMAC-SHA256 signed over the raw body in the X-Swiggy-Signature
// header and normalized to the shared aggregator-order event.
//
// Config keys: api_key, partner_id (Swiggy outlet id), webhook_secret.
type Swiggy struct {
	apiKey        string
	partnerID     string
	webhookSecret string
	baseURL       string
	client        httpDoer
}

// NewSwiggy constructs an uninitialized adapter; call Init or use the factory.
func NewSwiggy() *Swiggy {
	return &Swiggy{baseURL: swiggyBaseURL, client: newHTTPClient()}
}

func (s *Swiggy) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "swiggy",
		Name:         "Swiggy",
		Capabilities: []connector.Capability{connector.CapabilityAggregator},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["api_key", "partner_id", "webhook_secret"],
  "properties": {
    "api_key":        {"type": "string", "title": "API Key", "secret": true},
    "partner_id":     {"type": "string", "title": "Swiggy Partner/Outlet ID"},
    "webhook_secret": {"type": "string", "title": "Webhook Secret", "secret": true}
  }
}`),
	}
}

func (s *Swiggy) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "api_key", "partner_id", "webhook_secret"); err != nil {
		return err
	}
	s.apiKey = cfgGet(cfg, "api_key")
	s.partnerID = cfgGet(cfg, "partner_id")
	s.webhookSecret = cfgGet(cfg, "webhook_secret")
	if url := cfgGet(cfg, "base_url"); url != "" {
		s.baseURL = url
	}
	if s.baseURL == "" {
		s.baseURL = swiggyBaseURL
	}
	if s.client == nil {
		s.client = newHTTPClient()
	}
	return nil
}

// PushMenu publishes the normalized catalog (menuJSON) to Swiggy and returns the
// number of items accepted.
func (s *Swiggy) PushMenu(ctx context.Context, menuJSON []byte) (int, error) {
	var menu struct {
		Items []json.RawMessage `json:"items"`
	}
	_ = json.Unmarshal(menuJSON, &menu)

	req, err := jsonRequest(ctx, http.MethodPost,
		fmt.Sprintf("%s/outlets/%s/menu", s.baseURL, s.partnerID), json.RawMessage(menuJSON))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := doJSON(s.client, req, &out); err != nil {
		return 0, err
	}
	if out.Accepted > 0 {
		return out.Accepted, nil
	}
	return len(menu.Items), nil
}

// VerifyWebhook checks the X-Swiggy-Signature HMAC-SHA256 over the raw body and,
// on success, normalizes the order webhook to the shared aggregator-order event.
func (s *Swiggy) VerifyWebhook(_ context.Context, body []byte, headers map[string]string) (events.Event, error) {
	sig := header(headers, "X-Swiggy-Signature")
	expected := hmacSHA256Hex(s.webhookSecret, body)
	if !constantTimeEqualHex(sig, expected) {
		return events.Event{}, fmt.Errorf("swiggy: webhook signature mismatch")
	}
	return normalizeAggregatorOrder("swiggy", body)
}

var _ connector.AggregatorConnector = (*Swiggy)(nil)
