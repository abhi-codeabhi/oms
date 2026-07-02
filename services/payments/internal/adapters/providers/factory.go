// Package providers adapts the pkg/connectors provider factory to the app's
// ports.ProviderFactory / ports.PaymentProvider interfaces. This is the only place
// that imports the connector SDK; the app/domain stay free of it and drive the
// gateway through the small PaymentProvider port.
package providers

import (
	"context"
	"encoding/json"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/connectors"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/ports"
)

// Factory implements ports.ProviderFactory using pkg/connectors.NewPayment. It
// builds a concrete PaymentConnector for a resolved connector id + config, then
// wraps it in the app-facing PaymentProvider port.
type Factory struct{}

var _ ports.ProviderFactory = (*Factory)(nil)

// New builds a Factory (stateless; provider config comes per-call from the hub).
func New() *Factory { return &Factory{} }

// Payment instantiates the connector adapter and initializes it with cfg.
func (f *Factory) Payment(_ context.Context, connectorID string, cfg map[string]string) (ports.PaymentProvider, error) {
	// NewPayment builds AND Init's the adapter with the resolved per-tenant config.
	c, err := connectors.NewPayment(connectorID, cfg)
	if err != nil {
		return nil, err
	}
	return &provider{c: c}, nil
}

// provider wraps a pkg/connector.PaymentConnector to satisfy ports.PaymentProvider.
type provider struct{ c connector.PaymentConnector }

func (p *provider) CreateIntent(ctx context.Context, amount money.Money, ref string) (string, error) {
	return p.c.CreateIntent(ctx, amount, ref)
}

func (p *provider) Capture(ctx context.Context, providerRef string) (json.RawMessage, error) {
	return p.c.Capture(ctx, providerRef)
}

func (p *provider) Refund(ctx context.Context, providerRef string, amount money.Money) error {
	return p.c.Refund(ctx, providerRef, amount)
}
