package connectors

import (
	"context"
	"encoding/json"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// MockPay is a test payment connector that always succeeds and makes no network
// calls. It lets developers exercise the payments flow end-to-end (and CI run
// without secrets) by resolving to "mockpay" in test mode.
type MockPay struct{}

func NewMockPay() *MockPay { return &MockPay{} }

func (m *MockPay) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "mockpay",
		Name:         "Mock Payments (test)",
		Capabilities: []connector.Capability{connector.CapabilityPayment},
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

func (m *MockPay) Init(context.Context, map[string]string) error { return nil }

func (m *MockPay) CreateIntent(_ context.Context, _ money.Money, ref string) (string, error) {
	return "mock_" + ids.New("ord"), nil
}

func (m *MockPay) Capture(_ context.Context, provRef string) (json.RawMessage, error) {
	return json.RawMessage(`{"status":"captured","provider_ref":"` + provRef + `"}`), nil
}

func (m *MockPay) Refund(context.Context, string, money.Money) error { return nil }

func (m *MockPay) VerifyWebhook(_ context.Context, body []byte, _ string) (events.Event, error) {
	// Echo whatever provider_ref/order_id the mock webhook body carried so the
	// payments consumer can match it to a Payment (it drops events with no ref).
	var w struct {
		ProviderRef string `json:"provider_ref"`
		OrderID     string `json:"order_id"`
		Ref         string `json:"ref"`
	}
	_ = json.Unmarshal(body, &w)
	ref := firstNonEmpty(w.ProviderRef, w.OrderID, w.Ref)
	return events.New(EventPaymentCaptured, "", map[string]any{
		"connector_id": "mockpay",
		"provider_ref": ref,
		"status":       "captured",
	}), nil
}

var _ connector.PaymentConnector = (*MockPay)(nil)
