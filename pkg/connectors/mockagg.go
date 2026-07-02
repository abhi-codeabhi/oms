package connectors

import (
	"context"
	"encoding/json"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
)

// MockAgg is a dependency-free aggregator connector for local dev and tests. It
// implements connector.AggregatorConnector without any network calls: PushMenu
// counts the items it was handed, and VerifyWebhook accepts any body (no signature
// check) and normalizes it to the shared aggregator-order event. It lets the
// aggregators service and connector-hub be exercised end-to-end without a real
// Zomato/Swiggy account.
//
// Config keys: none required.
type MockAgg struct{}

// NewMockAgg constructs the mock aggregator adapter.
func NewMockAgg() *MockAgg { return &MockAgg{} }

func (m *MockAgg) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           "mockagg",
		Name:         "Mock Aggregator (dev)",
		Capabilities: []connector.Capability{connector.CapabilityAggregator},
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

// Init accepts any config (the mock needs none).
func (m *MockAgg) Init(_ context.Context, _ map[string]string) error { return nil }

// PushMenu counts the items in the normalized menu JSON and returns that count,
// as if the aggregator accepted them all.
func (m *MockAgg) PushMenu(_ context.Context, menuJSON []byte) (int, error) {
	var menu struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(menuJSON, &menu); err != nil {
		return 0, err
	}
	return len(menu.Items), nil
}

// VerifyWebhook skips signature verification (dev only) and normalizes the body to
// the shared aggregator-order event.
func (m *MockAgg) VerifyWebhook(_ context.Context, body []byte, _ map[string]string) (events.Event, error) {
	return normalizeAggregatorOrder("mockagg", body)
}

var _ connector.AggregatorConnector = (*MockAgg)(nil)
