// Package nats adapts the shared pkg/eventbus/nats bus to the app's
// ports.EventBus. IngestWebhook publishes normalized provider events directly to
// NATS (the webhook is not part of a DB transaction, so it does not go through the
// outbox relay).
package nats

import (
	"context"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// Bus implements ports.EventBus over an outbox.Bus (the JetStream publisher).
type Bus struct {
	bus outbox.Bus
}

var _ ports.EventBus = (*Bus)(nil)

// New wraps a connected outbox.Bus (from pkg/eventbus/nats.Connect).
func New(bus outbox.Bus) *Bus { return &Bus{bus: bus} }

// Publish implements ports.EventBus.
func (b *Bus) Publish(ctx context.Context, e events.Event) error {
	return b.bus.Publish(ctx, e)
}
