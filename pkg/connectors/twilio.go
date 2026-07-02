package connectors

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
)

// twilioBaseURL is the Twilio REST v2010 root; overridable in tests.
const twilioBaseURL = "https://api.twilio.com"

// Twilio implements connector.NotificationConnector for SMS and WhatsApp via the
// Messages resource. Auth is HTTP Basic (AccountSID:AuthToken). Status-callback
// webhooks are verified with Twilio's X-Twilio-Signature scheme:
// base64(HMAC-SHA1(authToken, url + sorted(post-params))).
//
// Config keys: account_sid, auth_token, from (E.164 sender or "whatsapp:+..."),
// optional status_callback_url (used only for signature verification).
type Twilio struct {
	accountSID  string
	authToken   string
	from        string
	callbackURL string
	baseURL     string
	client      httpDoer
}

func NewTwilio() *Twilio {
	return &Twilio{baseURL: twilioBaseURL, client: newHTTPClient()}
}

func (t *Twilio) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "twilio",
		Name:         "Twilio (SMS/WhatsApp)",
		Capabilities: []connector.Capability{connector.CapabilityNotification},
		ConfigSchema: json.RawMessage(`{
  "type": "object",
  "required": ["account_sid", "auth_token", "from"],
  "properties": {
    "account_sid":         {"type": "string", "title": "Account SID"},
    "auth_token":          {"type": "string", "title": "Auth Token", "secret": true},
    "from":                {"type": "string", "title": "From (E.164 or whatsapp:+...)"},
    "status_callback_url": {"type": "string", "title": "Status Callback URL"}
  }
}`),
	}
}

func (t *Twilio) Init(_ context.Context, cfg map[string]string) error {
	if err := requireCfg(cfg, "account_sid", "auth_token", "from"); err != nil {
		return err
	}
	t.accountSID = cfgGet(cfg, "account_sid")
	t.authToken = cfgGet(cfg, "auth_token")
	t.from = cfgGet(cfg, "from")
	t.callbackURL = cfgGet(cfg, "status_callback_url")
	if url := cfgGet(cfg, "base_url"); url != "" {
		t.baseURL = url
	}
	if t.baseURL == "" {
		t.baseURL = twilioBaseURL
	}
	if t.client == nil {
		t.client = newHTTPClient()
	}
	return nil
}

// Send posts a message. channel selects the address prefix: "whatsapp" wraps both
// from/to as whatsapp:+... ; subject is prepended to the SMS body (SMS has no
// subject). Returns the Twilio message SID.
func (t *Twilio) Send(ctx context.Context, channel, to, subject, body string) (string, error) {
	from, dest := t.from, to
	if strings.EqualFold(channel, "whatsapp") {
		from = ensurePrefix(from, "whatsapp:")
		dest = ensurePrefix(dest, "whatsapp:")
	}
	text := body
	if subject != "" {
		text = subject + "\n" + body
	}
	form := url.Values{"From": {from}, "To": {dest}, "Body": {text}}
	if t.callbackURL != "" {
		form.Set("StatusCallback", t.callbackURL)
	}
	endpoint := fmt.Sprintf("%s/2010-04-01/Accounts/%s/Messages.json", t.baseURL, t.accountSID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(t.accountSID, t.authToken)
	var out struct {
		SID string `json:"sid"`
	}
	if err := doJSON(t.client, req, &out); err != nil {
		return "", err
	}
	if out.SID == "" {
		return "", fmt.Errorf("twilio: empty message sid")
	}
	return out.SID, nil
}

// VerifyWebhook validates the X-Twilio-Signature over the callback URL + sorted
// form params, then normalizes the delivery status to an event. Twilio posts
// form-encoded status callbacks; the raw body is the urlencoded form.
func (t *Twilio) VerifyWebhook(_ context.Context, body []byte, headers map[string]string) (events.Event, error) {
	sig := header(headers, "X-Twilio-Signature")
	callbackURL := header(headers, "X-Restorna-Callback-Url")
	if callbackURL == "" {
		callbackURL = t.callbackURL
	}
	params, err := url.ParseQuery(string(body))
	if err != nil {
		return events.Event{}, fmt.Errorf("twilio: parse callback: %w", err)
	}
	if !twilioSigValid(t.authToken, callbackURL, params, sig) {
		return events.Event{}, fmt.Errorf("twilio: signature mismatch")
	}
	return events.New("restorna.notifications.status.v1", "", map[string]any{
		"connector_id": "twilio",
		"provider_ref": params.Get("MessageSid"),
		"status":       params.Get("MessageStatus"),
		"to":           params.Get("To"),
	}), nil
}

// twilioSigValid recomputes Twilio's request signature and compares in constant time.
func twilioSigValid(authToken, callbackURL string, params url.Values, sig string) bool {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(callbackURL)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(params.Get(k))
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(sig))
}

func ensurePrefix(s, prefix string) string {
	if strings.HasPrefix(s, prefix) {
		return s
	}
	return prefix + s
}

var _ connector.NotificationConnector = (*Twilio)(nil)
