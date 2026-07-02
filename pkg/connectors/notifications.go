package connectors

import (
	"context"
	"fmt"
	"sort"

	"github.com/restorna/platform/pkg/connector"
)

// LogNotifyID is the manifest id of the built-in mock notification connector. The
// notifications service resolves to it when a tenant has installed no real provider
// so identity OTP and staff invites still "work" in dev without credentials.
const LogNotifyID = "lognotify"

// notificationBuilders is the registry of built-in notification providers keyed by
// connector id (matches connector-hub Manifest.ID / ResolveResponse.connector_id).
// Adding a provider is additive: implement connector.NotificationConnector and
// register its constructor here.
var notificationBuilders = map[string]func() connector.NotificationConnector{
	"twilio":  func() connector.NotificationConnector { return NewTwilio() },
	"msg91":   func() connector.NotificationConnector { return NewMSG91() },
	LogNotifyID: func() connector.NotificationConnector { return NewLogNotify() },
}

// NewNotification instantiates the notification connector registered under id and
// initializes it with cfg (decrypted per-tenant config from connector-hub's
// Resolve). It is the counterpart to New (payments): the notifications service
// calls it to turn a resolved provider id + config into a live adapter. Unknown ids
// return an error so the caller can fall back to the built-in lognotify mock.
func NewNotification(id string, cfg map[string]string) (connector.NotificationConnector, error) {
	b, ok := notificationBuilders[id]
	if !ok {
		return nil, fmt.Errorf("connectors: unknown notification connector %q", id)
	}
	c := b()
	if err := c.Init(context.Background(), cfg); err != nil {
		return nil, fmt.Errorf("connectors: init %q: %w", id, err)
	}
	return c, nil
}

// RegisterNotification adds/overrides a notification builder (tests or plugins).
func RegisterNotification(id string, b func() connector.NotificationConnector) {
	notificationBuilders[id] = b
}

// NotificationIDs lists the registered notification connector ids (sorted).
func NotificationIDs() []string {
	out := make([]string, 0, len(notificationBuilders))
	for id := range notificationBuilders {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
