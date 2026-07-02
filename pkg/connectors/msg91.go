package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
)

// msg91BaseURL is the MSG91 v5 API root; overridable in tests.
const msg91BaseURL = "https://control.msg91.com/api/v5"

// MSG91 implements connector.NotificationConnector for SMS via MSG91's Flow API.
// Auth is the "authkey" header. Delivery-report webhooks are verified with a
// shared token compared against an X-MSG91-Token header (MSG91 DLR posts don't
// carry an HMAC; a per-tenant shared secret is the documented guard).
//
// Config keys: auth_key, sender_id, template_id (DLT template), optional
// webhook_token.
type MSG91 struct {
	authKey      string
	senderID     string
	templateID   string
	webhookToken string
	baseURL      string
	client       httpDoer
}

func NewMSG91() *MSG91 {
	return &MSG91{baseURL: msg91BaseURL, client: newHTTPClient()}
}

func (m *MSG91) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "msg91",
		Name:         "MSG91 (SMS)",
		Capabilities: []connector.Capability{connector.CapabilityNotification},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["auth_key", "sender_id", "template_id"],
  "properties": {
    "auth_key":      {"type": "string", "title": "Auth Key", "secret": true},
    "sender_id":     {"type": "string", "title": "Sender ID"},
    "template_id":   {"type": "string", "title": "DLT Template ID"},
    "webhook_token": {"type": "string", "title": "Webhook Token", "secret": true}
  }
}`),
	}
}

func (m *MSG91) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "auth_key", "sender_id", "template_id"); err != nil {
		return err
	}
	m.authKey = cfgGet(cfg, "auth_key")
	m.senderID = cfgGet(cfg, "sender_id")
	m.templateID = cfgGet(cfg, "template_id")
	m.webhookToken = cfgGet(cfg, "webhook_token")
	if url := cfgGet(cfg, "base_url"); url != "" {
		m.baseURL = url
	}
	if m.baseURL == "" {
		m.baseURL = msg91BaseURL
	}
	if m.client == nil {
		m.client = newHTTPClient()
	}
	return nil
}

// Send dispatches an SMS via the Flow API. The body is passed as the template's
// first variable ("body"); subject is ignored for SMS. Returns MSG91's request id.
func (m *MSG91) Send(ctx context.Context, channel, to, subject, body string) (string, error) {
	payload := map[string]any{
		"template_id": m.templateID,
		"sender":      m.senderID,
		"short_url":   "0",
		"recipients": []map[string]string{
			{"mobiles": to, "body": body},
		},
	}
	req, err := jsonRequest(ctx, http.MethodPost, m.baseURL+"/flow/", payload)
	if err != nil {
		return "", err
	}
	req.Header.Set("authkey", m.authKey)
	var out struct {
		Type      string `json:"type"`
		RequestID string `json:"request_id"`
		Message   string `json:"message"`
	}
	if err := doJSON(m.client, req, &out); err != nil {
		return "", err
	}
	if out.Type != "" && out.Type != "success" {
		return "", fmt.Errorf("msg91: send failed: %s", out.Message)
	}
	ref := out.RequestID
	if ref == "" {
		ref = out.Message
	}
	return ref, nil
}

// VerifyWebhook checks the shared webhook token (constant-time) and normalizes the
// delivery report to an event.
func (m *MSG91) VerifyWebhook(_ context.Context, body []byte, headers map[string]string) (events.Event, error) {
	if m.webhookToken != "" {
		got := header(headers, "X-MSG91-Token")
		if !constantTimeEqualHex(got, m.webhookToken) {
			return events.Event{}, fmt.Errorf("msg91: webhook token mismatch")
		}
	}
	var dlr struct {
		RequestID string `json:"requestId"`
		Report    []struct {
			Number string `json:"number"`
			Status string `json:"status"`
		} `json:"report"`
	}
	_ = json.Unmarshal(body, &dlr)
	status, number := "", ""
	if len(dlr.Report) > 0 {
		status = dlr.Report[0].Status
		number = dlr.Report[0].Number
	}
	return events.New("restorna.notifications.status.v1", "", map[string]any{
		"connector_id": "msg91",
		"provider_ref": dlr.RequestID,
		"status":       status,
		"to":           number,
	}), nil
}

var _ connector.NotificationConnector = (*MSG91)(nil)
