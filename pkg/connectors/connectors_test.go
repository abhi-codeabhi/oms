package connectors_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/restorna/platform/pkg/connectors"
	"github.com/restorna/platform/pkg/money"
)

// TestNewPaymentUnknownConnector: the payment factory rejects an unregistered id.
func TestNewPaymentUnknownConnector(t *testing.T) {
	if _, err := connectors.NewPayment("nope", nil); err == nil {
		t.Fatalf("expected error for unknown connector")
	}
}

// TestNewPaymentMockRoundTrip: NewPayment("mock") resolves the always-succeeds
// MockPay adapter, exercising CreateIntent -> Capture -> Refund with no credentials.
func TestNewPaymentMockRoundTrip(t *testing.T) {
	c, err := connectors.NewPayment("mock", nil)
	if err != nil {
		t.Fatalf("New mock: %v", err)
	}
	ref, err := c.CreateIntent(context.Background(), money.New(1000, "INR"), "pay_1")
	if err != nil || ref == "" {
		t.Fatalf("CreateIntent: ref=%q err=%v", ref, err)
	}
	if _, err := c.Capture(context.Background(), ref); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if err := c.Refund(context.Background(), ref, money.New(500, "INR")); err != nil {
		t.Fatalf("Refund: %v", err)
	}
}

// TestNewPaymentRazorpayRequiresConfig: NewPayment surfaces Init's config validation.
func TestNewPaymentRazorpayRequiresConfig(t *testing.T) {
	if _, err := connectors.NewPayment("razorpay", map[string]string{"key_id": "k"}); err == nil {
		t.Fatalf("expected error for razorpay missing key_secret/webhook_secret")
	}
}

// TestPaymentRazorpayWebhookHMAC: a fully-configured Razorpay adapter verifies its
// X-Razorpay-Signature HMAC and normalizes the webhook to a payment event.
func TestPaymentRazorpayWebhookHMAC(t *testing.T) {
	secret := "whsec_test"
	c, err := connectors.NewPayment("razorpay", map[string]string{
		"key_id": "k", "key_secret": "s", "webhook_secret": secret,
	})
	if err != nil {
		t.Fatalf("New razorpay: %v", err)
	}

	body := []byte(`{"event":"payment.captured","payload":{"payment":{"entity":{"id":"pay_x","order_id":"razorpay_ord_9","status":"captured","amount":1000,"currency":"INR"}}}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	e, err := c.VerifyWebhook(context.Background(), body, sig)
	if err != nil {
		t.Fatalf("VerifyWebhook (valid sig): %v", err)
	}
	var d struct {
		ProviderRef string `json:"provider_ref"`
		Status      string `json:"status"`
	}
	if err := json.Unmarshal(e.Data, &d); err != nil {
		t.Fatalf("decode normalized data: %v", err)
	}
	if d.ProviderRef != "razorpay_ord_9" {
		t.Errorf("provider_ref = %q, want razorpay_ord_9", d.ProviderRef)
	}

	// A bad signature is rejected.
	if _, err := c.VerifyWebhook(context.Background(), body, "deadbeef"); err == nil {
		t.Errorf("expected signature mismatch error")
	}
}
