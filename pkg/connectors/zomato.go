package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
)

// zomatoBaseURL is the Zomato partner API root; overridable in tests/config.
const zomatoBaseURL = "https://partners.zomato.com/v1"

// Zomato implements connector.AggregatorConnector against Zomato's partner API.
// Menu push is a single JSON POST of the normalized catalog; inbound order
// webhooks are signed with HMAC-SHA256 over the raw body and delivered in the
// X-Zomato-Signature header. On verification the webhook is normalized to a
// restorna.connector.aggregator.order.received event that connector-hub publishes
// and the aggregators service consumes.
//
// Config keys: api_key, restaurant_id (Zomato res id), webhook_secret.
type Zomato struct {
	apiKey        string
	zomatoResID   string
	webhookSecret string
	baseURL       string
	client        httpDoer
}

// NewZomato constructs an uninitialized adapter; call Init or use the factory.
func NewZomato() *Zomato {
	return &Zomato{baseURL: zomatoBaseURL, client: newHTTPClient()}
}

func (z *Zomato) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "zomato",
		Name:         "Zomato",
		Capabilities: []connector.Capability{connector.CapabilityAggregator},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["api_key", "restaurant_id", "webhook_secret"],
  "properties": {
    "api_key":        {"type": "string", "title": "API Key", "secret": true},
    "restaurant_id":  {"type": "string", "title": "Zomato Restaurant ID"},
    "webhook_secret": {"type": "string", "title": "Webhook Secret", "secret": true}
  }
}`),
	}
}

func (z *Zomato) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "api_key", "restaurant_id", "webhook_secret"); err != nil {
		return err
	}
	z.apiKey = cfgGet(cfg, "api_key")
	z.zomatoResID = cfgGet(cfg, "restaurant_id")
	z.webhookSecret = cfgGet(cfg, "webhook_secret")
	if url := cfgGet(cfg, "base_url"); url != "" {
		z.baseURL = url
	}
	if z.baseURL == "" {
		z.baseURL = zomatoBaseURL
	}
	if z.client == nil {
		z.client = newHTTPClient()
	}
	return nil
}

// PushMenu publishes the normalized catalog (menuJSON) to Zomato and returns the
// number of items accepted. menuJSON is the aggregators service's serialized menu
// ({"items":[...]}); Zomato echoes an accepted count.
func (z *Zomato) PushMenu(ctx context.Context, menuJSON []byte) (int, error) {
	var menu struct {
		Items []json.RawMessage `json:"items"`
	}
	// Best-effort local count for the fallback/return value.
	_ = json.Unmarshal(menuJSON, &menu)

	req, err := jsonRequest(ctx, http.MethodPost,
		fmt.Sprintf("%s/restaurants/%s/menu", z.baseURL, z.zomatoResID), json.RawMessage(menuJSON))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+z.apiKey)
	var out struct {
		Accepted int `json:"accepted"`
	}
	if err := doJSON(z.client, req, &out); err != nil {
		return 0, err
	}
	if out.Accepted > 0 {
		return out.Accepted, nil
	}
	return len(menu.Items), nil
}

// VerifyWebhook checks the X-Zomato-Signature HMAC-SHA256 over the raw body and,
// on success, normalizes the order webhook to the shared aggregator-order event.
func (z *Zomato) VerifyWebhook(_ context.Context, body []byte, headers map[string]string) (events.Event, error) {
	sig := header(headers, "X-Zomato-Signature")
	expected := hmacSHA256Hex(z.webhookSecret, body)
	if !constantTimeEqualHex(sig, expected) {
		return events.Event{}, fmt.Errorf("zomato: webhook signature mismatch")
	}
	return normalizeAggregatorOrder("zomato", body)
}

var _ connector.AggregatorConnector = (*Zomato)(nil)
