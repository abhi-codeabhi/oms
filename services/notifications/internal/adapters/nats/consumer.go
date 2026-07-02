// Package nats wires the delivery-status webhook consumer. connector-hub ingests a
// provider's delivery-report webhook, verifies it via the connector, and publishes a
// normalized status event; this consumer subscribes to those events and advances the
// matching message's DeliveryStatus (QUEUED/SENT -> DELIVERED/FAILED). Consumers
// dedupe on the CloudEvent id (processed_events) for exactly-once effect.
package nats

import (
	"context"
	"encoding/json"

	"github.com/rs/zerolog/log"

	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/services/notifications/internal/app"
	"github.com/restorna/platform/services/notifications/internal/domain"
)

// DeliveryStatusEventTypes are the normalized event types notification connectors
// emit from VerifyWebhook (see pkg/connectors twilio/msg91/lognotify). connector-hub
// re-publishes them; the consumer subscribes to each.
var DeliveryStatusEventTypes = []string{
	"restorna.notifications.status.v1",
	"restorna.notifications.delivery.updated.v1",
}

// statusPayload is the normalized delivery-status shape carried in events.Event.Data.
type statusPayload struct {
	ConnectorID string `json:"connector_id"`
	ProviderRef string `json:"provider_ref"`
	Status      string `json:"status"`
}

// Consumer applies delivery-status events to messages via the app.
type Consumer struct {
	uc  *app.App
	url string
}

// NewConsumer builds a delivery-status consumer bound to the NATS url.
func NewConsumer(uc *app.App, natsURL string) *Consumer {
	return &Consumer{uc: uc, url: natsURL}
}

// Run subscribes to every delivery-status event type with a durable queue and blocks
// until ctx is cancelled. Each event is deduped by id in the app (processed_events).
func (c *Consumer) Run(ctx context.Context) error {
	for _, typ := range DeliveryStatusEventTypes {
		typ := typ
		if err := eventbus.Subscribe(ctx, c.url, typ, "notifications-delivery", func(e events.Event) error {
			return c.handle(ctx, e)
		}); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

// handle maps one normalized status event to app.ApplyDeliveryStatus.
func (c *Consumer) handle(ctx context.Context, e events.Event) error {
	var p statusPayload
	if err := json.Unmarshal(e.Data, &p); err != nil {
		log.Warn().Err(err).Str("event_id", e.ID).Msg("notifications: bad delivery-status payload")
		return nil // poison message: ack + drop rather than block the stream
	}
	status := normalizeStatus(p.Status)
	if status == domain.StatusUnspecified || p.ProviderRef == "" {
		return nil
	}
	return c.uc.ApplyDeliveryStatus(ctx, e.ID, p.ConnectorID, p.ProviderRef, status)
}

// normalizeStatus maps provider status strings to a domain DeliveryStatus.
func normalizeStatus(s string) domain.DeliveryStatus {
	switch s {
	case "delivered", "read":
		return domain.StatusDelivered
	case "failed", "undelivered", "rejected":
		return domain.StatusFailed
	case "sent":
		return domain.StatusSent
	default:
		return domain.StatusUnspecified
	}
}
