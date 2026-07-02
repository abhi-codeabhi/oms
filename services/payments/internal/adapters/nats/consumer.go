// Package nats holds the event consumer that drives the payments webhook
// choreography: it subscribes to the normalized payment webhook events that
// connector-hub publishes (restorna.connector.payment.captured / .failed), matches
// them to a Payment by provider_ref, flips status to CAPTURED/FAILED, and emits
// restorna.payments.captured.v1 / .failed.v1. Delivery is idempotent: pkg/eventbus
// dedupes on Event.ID in process and the app marks the event id processed in the
// same tx as the status flip, so a redelivery (even across restarts) is a no-op.
package nats

import (
	"context"
	"encoding/json"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/events"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/payments/internal/app"
)

// Subjects the payments service subscribes to (published by connector-hub after it
// verifies a provider webhook via the connector's VerifyWebhook).
const (
	SubjectPaymentCaptured = "restorna.connector.payment.captured"
	SubjectPaymentFailed   = "restorna.connector.payment.failed"
)

// durable prefixes (one logical payments consumer per subject).
const (
	durableCaptured = "payments-webhook-captured"
	durableFailed   = "payments-webhook-failed"
)

// webhookData is the normalized JSON shape of the connector-hub payment webhook
// payload: { provider_ref, status, method, ... }. Emitted by the payment
// connectors' VerifyWebhook (see pkg/connectors) and re-published by the hub.
type webhookData struct {
	ProviderRef string `json:"provider_ref"`
	Status      string `json:"status"` // captured|failed|refunded
	Method      string `json:"method"`
}

// Consumer wires the payment-webhook subscriptions to the app use case.
type Consumer struct {
	uc      *app.App
	natsURL string
}

// New builds a Consumer for the given app and NATS url.
func New(uc *app.App, natsURL string) *Consumer {
	return &Consumer{uc: uc, natsURL: natsURL}
}

// Run subscribes to both captured + failed subjects and blocks until ctx is done.
// Intended to run as a goroutine from main.
func (c *Consumer) Run(ctx context.Context) error {
	errc := make(chan error, 2)
	go func() {
		errc <- eventbus.Subscribe(ctx, c.natsURL, SubjectPaymentCaptured, durableCaptured, func(e events.Event) error {
			return c.handle(ctx, e, true)
		})
	}()
	go func() {
		errc <- eventbus.Subscribe(ctx, c.natsURL, SubjectPaymentFailed, durableFailed, func(e events.Event) error {
			return c.handle(ctx, e, false)
		})
	}()
	// Return the first error (or nil on clean ctx cancel from either subscription).
	if err := <-errc; err != nil && ctx.Err() == nil {
		return err
	}
	return nil
}

// handle decodes one normalized webhook event and applies it. Pure mapping +
// tenancy wiring; the status machine + persistence live in the app.
func (c *Consumer) handle(ctx context.Context, e events.Event, captured bool) error {
	var d webhookData
	if err := json.Unmarshal(e.Data, &d); err != nil {
		// Poison payload: ack/drop it so a malformed event never wedges the consumer.
		return nil
	}
	if d.ProviderRef == "" {
		return nil
	}

	// Scope the repo tx to the event's tenant when present (the app also resolves
	// the true tenant from the matched payment). Never trust a body id for tenancy.
	if e.TenantID != "" {
		ctx = tenancy.With(ctx, tenancy.Scope{
			RestaurantID: e.TenantID,
			Role:         commonv1.Role_ROLE_CASHIER,
		})
	}

	if err := c.uc.OnWebhook(ctx, app.WebhookEvent{
		EventID:      e.ID,
		RestaurantID: e.TenantID,
		ProviderRef:  d.ProviderRef,
		Captured:     captured,
		Method:       d.Method,
	}); err != nil {
		return fmt.Errorf("payments webhook consumer: ref %s: %w", d.ProviderRef, err)
	}
	return nil
}
