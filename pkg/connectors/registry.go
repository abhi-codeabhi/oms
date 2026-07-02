package connectors

import (
	"context"
	"fmt"
	"sort"

	"github.com/restorna/platform/pkg/connector"
)

// factories is the unified registry of every built-in connector constructor keyed
// by manifest id (payments + notifications + aggregators + mocks). Each returns a
// fresh, uninitialized adapter; New (below) Inits it with the tenant's stored
// config in one step. Adding a vendor is additive: register a constructor here and
// it becomes installable everywhere (connector-hub marketplace, payments,
// notifications, aggregators) with no core changes.
//
// The capability-typed factories (NewPayment in connectors.go, NewNotification in
// notifications.go) are thin conveniences over the same adapters for callers that
// want a concrete capability interface without a type assertion.
var factories = map[string]func() connector.Connector{
	// Payments
	"razorpay": func() connector.Connector { return NewRazorpay() },
	"paytm":    func() connector.Connector { return NewPaytm() },
	"phonepe":  func() connector.Connector { return NewPhonePe() },
	"mockpay":  func() connector.Connector { return NewMockPay() },
	// Notifications
	"twilio":    func() connector.Connector { return NewTwilio() },
	"msg91":     func() connector.Connector { return NewMSG91() },
	"lognotify": func() connector.Connector { return NewLogNotify() },
	// Aggregators
	"zomato":  func() connector.Connector { return NewZomato() },
	"swiggy":  func() connector.Connector { return NewSwiggy() },
	"mockagg": func() connector.Connector { return NewMockAgg() },
}

// New instantiates the connector registered under id and initializes it with cfg
// (the decrypted per-tenant config from connector-hub's Resolve). It is the single
// factory connector-hub, payments, notifications and aggregators use to turn a
// stored config map into a live adapter. Unknown ids return an error; Init failures
// (e.g. missing required config keys) propagate so the caller can surface a clear
// FailedPrecondition upstream.
//
// The returned connector.Connector is type-asserted by the caller to the capability
// interface it needs (PaymentConnector, NotificationConnector, AggregatorConnector).
func New(id string, cfg map[string]string) (connector.Connector, error) {
	mk, ok := factories[id]
	if !ok {
		return nil, fmt.Errorf("connectors: unknown connector %q", id)
	}
	c := mk()
	if err := c.Init(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("connectors: init %q: %w", id, err)
	}
	return c, nil
}

// All returns the manifests of every built-in connector, sorted by id. The
// connector-hub uses this to seed its marketplace listing (ListAvailable).
func All() []connector.Manifest {
	out := make([]connector.Manifest, 0, len(factories))
	for _, mk := range factories {
		out = append(out, mk().Manifest())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// IDs lists every registered connector id (sorted; for diagnostics/tests).
func IDs() []string {
	ids := make([]string, 0, len(factories))
	for id := range factories {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
