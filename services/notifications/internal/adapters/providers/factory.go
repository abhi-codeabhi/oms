// Package providers adapts pkg/connectors to the app's ports.ProviderFactory. It is
// the only place that knows about the concrete provider adapters; the app depends on
// the ports.NotificationSender interface. New instantiates a real provider (twilio/
// msg91/...) from a resolved id + config; Fallback returns the built-in lognotify
// mock so identity OTP / staff invites still "work" in dev with no provider.
package providers

import (
	"context"

	"github.com/restorna/platform/pkg/connectors"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

// Factory implements ports.ProviderFactory over pkg/connectors.
type Factory struct{}

var _ ports.ProviderFactory = (*Factory)(nil)

// New builds the notifications factory.
func New() *Factory { return &Factory{} }

// New instantiates the notification connector for connectorID with cfg. It returns
// an error for an unknown id or invalid config; the app then falls back to the mock.
func (f *Factory) New(_ context.Context, connectorID string, cfg map[string]string) (ports.NotificationSender, error) {
	c, err := connectors.NewNotification(connectorID, cfg)
	if err != nil {
		return nil, err
	}
	return c, nil
}

// Fallback returns the built-in lognotify mock sender and its connector id. It never
// fails, so OTP/invite flows always have a working sender in dev.
func (f *Factory) Fallback(_ context.Context) (ports.NotificationSender, string) {
	return connectors.NewLogNotify(), connectors.LogNotifyID
}
