package connectors

import (
	"context"
	"encoding/json"
	"sync/atomic"

	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/ids"
)

// LogNotify is a mock NotificationConnector: instead of hitting a real provider it
// logs the message and returns a synthetic provider reference. It is the built-in
// fallback the notifications service uses when a tenant has installed no real
// notification provider — so identity OTP and staff invites still "work" in dev
// without any credentials.
//
// It requires no config (Init always succeeds), which is what lets the
// notifications service instantiate it unconditionally as a last resort.
type LogNotify struct {
	sent int64 // count of messages "delivered" (observable in tests)
}

// NewLogNotify constructs the mock connector.
func NewLogNotify() *LogNotify { return &LogNotify{} }

func (l *LogNotify) Manifest() connector.Manifest {
	return connector.Manifest{
		ID:           LogNotifyID,
		Name:         "Log (dev mock)",
		Capabilities: []connector.Capability{connector.CapabilityNotification},
		ConfigSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	}
}

// Init accepts any config (including none); the mock never authenticates.
func (l *LogNotify) Init(_ context.Context, _ map[string]string) error { return nil }

// Send logs the outbound message and returns a synthetic provider reference of the
// form "logn_<ulid>". It never fails, so OTP/invite flows always succeed in dev.
func (l *LogNotify) Send(_ context.Context, channel, to, subject, body string) (string, error) {
	atomic.AddInt64(&l.sent, 1)
	ref := ids.New("logn")
	log.Info().
		Str("connector", LogNotifyID).
		Str("channel", channel).
		Str("to", to).
		Str("subject", subject).
		Str("body", body).
		Str("provider_ref", ref).
		Msg("lognotify: message dispatched (mock)")
	return ref, nil
}

// VerifyWebhook synthesizes a delivered event for the given provider_ref carried in
// the body ({"provider_ref":"..."}). The mock trusts any caller (no signature), so
// it is only ever wired in dev.
func (l *LogNotify) VerifyWebhook(_ context.Context, body []byte, _ map[string]string) (events.Event, error) {
	var payload struct {
		ProviderRef string `json:"provider_ref"`
		Status      string `json:"status"`
	}
	_ = json.Unmarshal(body, &payload)
	if payload.Status == "" {
		payload.Status = "delivered"
	}
	return events.New("restorna.notifications.delivery.updated.v1", "", map[string]any{
		"connector_id": LogNotifyID,
		"provider_ref": payload.ProviderRef,
		"status":       payload.Status,
	}), nil
}

// Count reports how many messages the mock has dispatched (test observability).
func (l *LogNotify) Count() int64 { return atomic.LoadInt64(&l.sent) }

var _ connector.NotificationConnector = (*LogNotify)(nil)
