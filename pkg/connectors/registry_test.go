package connectors_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/connectors"
)

// TestAllManifests: All() lists every built-in connector, each with a non-empty id,
// name, exactly one capability, and a JSON config schema.
func TestAllManifests(t *testing.T) {
	ms := connectors.All()
	want := []string{
		"lognotify", "mockagg", "mockpay", "msg91", "paytm",
		"phonepe", "razorpay", "swiggy", "twilio", "zomato",
	}
	got := make([]string, 0, len(ms))
	for _, m := range ms {
		if m.ID == "" || m.Name == "" {
			t.Errorf("manifest missing id/name: %+v", m)
		}
		if len(m.Capabilities) != 1 {
			t.Errorf("%s: want exactly 1 capability, got %v", m.ID, m.Capabilities)
		}
		if len(m.ConfigSchema) == 0 {
			t.Errorf("%s: empty config schema", m.ID)
		}
		got = append(got, m.ID)
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("All() ids = %v, want %v", got, want)
	}
}

// TestFactoryReturnsCorrectType: New(id) instantiates an adapter that satisfies the
// capability interface implied by its id, across all three capabilities and mocks.
func TestFactoryReturnsCorrectType(t *testing.T) {
	cases := []struct {
		id  string
		cfg map[string]string
		cap string // "payment" | "notification" | "aggregator"
	}{
		{"mockpay", nil, "payment"},
		{"razorpay", map[string]string{"key_id": "k", "key_secret": "s", "webhook_secret": "w"}, "payment"},
		{"paytm", map[string]string{"mid": "m", "merchant_key": "mk"}, "payment"},
		{"phonepe", map[string]string{"merchant_id": "m", "salt_key": "s", "salt_index": "1"}, "payment"},
		{"lognotify", nil, "notification"},
		{"twilio", map[string]string{"account_sid": "AC", "auth_token": "t", "from": "+1"}, "notification"},
		{"msg91", map[string]string{"auth_key": "a", "sender_id": "s", "template_id": "t"}, "notification"},
		{"mockagg", nil, "aggregator"},
		{"zomato", map[string]string{"api_key": "a", "restaurant_id": "r", "webhook_secret": "w"}, "aggregator"},
		{"swiggy", map[string]string{"api_key": "a", "partner_id": "p", "webhook_secret": "w"}, "aggregator"},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			c, err := connectors.New(tc.id, tc.cfg)
			if err != nil {
				t.Fatalf("New(%q): %v", tc.id, err)
			}
			if c.Manifest().ID != tc.id {
				t.Errorf("manifest id = %q, want %q", c.Manifest().ID, tc.id)
			}
			switch tc.cap {
			case "payment":
				if _, ok := c.(connector.PaymentConnector); !ok {
					t.Errorf("%s: not a PaymentConnector", tc.id)
				}
			case "notification":
				if _, ok := c.(connector.NotificationConnector); !ok {
					t.Errorf("%s: not a NotificationConnector", tc.id)
				}
			case "aggregator":
				if _, ok := c.(connector.AggregatorConnector); !ok {
					t.Errorf("%s: not an AggregatorConnector", tc.id)
				}
			}
		})
	}
}

// TestNewUnknownID: the unified factory rejects an unregistered id.
func TestNewUnknownID(t *testing.T) {
	if _, err := connectors.New("does-not-exist", nil); err == nil {
		t.Fatal("expected error for unknown connector id")
	}
}

// TestNewInitError: the factory surfaces Init's required-config validation.
func TestNewInitError(t *testing.T) {
	if _, err := connectors.New("twilio", map[string]string{"account_sid": "AC"}); err == nil {
		t.Fatal("expected error for twilio missing auth_token/from")
	}
}

// TestSwiggyWebhookSignature: a correctly-signed aggregator webhook is accepted and
// a tampered body is rejected (second HMAC verification path).
func TestSwiggyWebhookSignature(t *testing.T) {
	c, err := connectors.New("swiggy", map[string]string{
		"api_key": "a", "partner_id": "p", "webhook_secret": "swsecret",
	})
	if err != nil {
		t.Fatalf("New swiggy: %v", err)
	}
	agg, ok := c.(connector.AggregatorConnector)
	if !ok {
		t.Fatal("swiggy is not an AggregatorConnector")
	}

	body := []byte(`{"order_id":"SW-42","status":"received","currency":"INR","items":[{"name":"Dosa","qty":1,"price_minor":12000}]}`)
	mac := hmac.New(sha256.New, []byte("swsecret"))
	mac.Write(body)
	good := hex.EncodeToString(mac.Sum(nil))

	if _, err := agg.VerifyWebhook(context.Background(), body, map[string]string{"X-Swiggy-Signature": good}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	// Tamper the body: the same signature must now fail.
	tampered := []byte(`{"order_id":"SW-42","status":"received","currency":"INR","items":[{"name":"Dosa","qty":9,"price_minor":12000}]}`)
	if _, err := agg.VerifyWebhook(context.Background(), tampered, map[string]string{"X-Swiggy-Signature": good}); err == nil {
		t.Fatal("tampered body accepted")
	}
}

// TestTwilioWebhookSignature: Twilio's X-Twilio-Signature (base64 HMAC-SHA1 over the
// callback URL + sorted form params) is accepted when correct and rejected when not.
func TestTwilioWebhookSignature(t *testing.T) {
	const callback = "https://hooks.restorna.app/twilio"
	c, err := connectors.New("twilio", map[string]string{
		"account_sid": "AC", "auth_token": "tok", "from": "+1555",
		"status_callback_url": callback,
	})
	if err != nil {
		t.Fatalf("New twilio: %v", err)
	}
	nc := c.(connector.NotificationConnector)

	form := url.Values{"MessageSid": {"SM1"}, "MessageStatus": {"delivered"}, "To": {"+91"}}
	body := form.Encode()

	// Recompute Twilio's signature: sort keys, concat url+key+value, HMAC-SHA1, base64.
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(callback)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString(form.Get(k))
	}
	mac := hmac.New(sha1.New, []byte("tok"))
	mac.Write([]byte(b.String()))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	if _, err := nc.VerifyWebhook(context.Background(), []byte(body), map[string]string{"X-Twilio-Signature": sig}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if _, err := nc.VerifyWebhook(context.Background(), []byte(body), map[string]string{"X-Twilio-Signature": "wrong"}); err == nil {
		t.Fatal("bad signature accepted")
	}
}
