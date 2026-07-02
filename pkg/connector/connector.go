// Package connector defines the plug-and-play integration SDK: the Connector
// contract, capability-specific extensions (e.g. PaymentConnector), and an
// in-memory Registry. The integration plane (connector-hub, payments) discovers
// and routes to connectors through these interfaces — adding a vendor is
// additive, with no core changes.
package connector

import (
	"context"
	"encoding/json"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/money"
)

// Capability tags what a connector can do.
type Capability string

const (
	CapabilityPayment      Capability = "payment"
	CapabilityAggregator   Capability = "aggregator"
	CapabilityCRM          Capability = "crm"
	CapabilityERP          Capability = "erp"
	CapabilityNotification Capability = "notification"
)

// Manifest declares a connector's identity, capabilities, and config schema.
type Manifest struct {
	ID           string          `json:"id"`
	Name         string          `json:"name"`
	Capabilities []Capability    `json:"capabilities"`
	ConfigSchema json.RawMessage `json:"config_schema,omitempty"`
}

// Connector is the contract every integration implements.
type Connector interface {
	Manifest() Manifest
	Init(ctx context.Context, cfg map[string]string) error
}

// PaymentConnector adds payment orchestration operations.
type PaymentConnector interface {
	Connector
	CreateIntent(ctx context.Context, amount money.Money, ref string) (provRef string, err error)
	Capture(ctx context.Context, provRef string) (receipt json.RawMessage, err error)
	Refund(ctx context.Context, provRef string, amount money.Money) error
	VerifyWebhook(ctx context.Context, body []byte, sig string) (events.Event, error)
}

// WebhookVerifier is the shared inbound-webhook shape for connectors that receive
// provider callbacks with a set of transport headers (rather than a single
// pre-extracted signature). The connector-hub passes the raw body and headers
// exactly as received; the connector pulls its own signature header, verifies it,
// and returns a normalized CloudEvent to publish. PaymentConnector predates this
// shape and keeps its (body, sig) form for backward compatibility.
type WebhookVerifier interface {
	VerifyWebhook(ctx context.Context, body []byte, headers map[string]string) (events.Event, error)
}

// NotificationConnector adds outbound-message operations (SMS/WhatsApp/email) and
// inbound delivery-status webhook verification.
type NotificationConnector interface {
	Connector
	WebhookVerifier
	// Send delivers a message over channel ("sms"|"whatsapp"|"email") to the
	// recipient and returns the provider's message reference.
	Send(ctx context.Context, channel, to, subject, body string) (providerRef string, err error)
}

// AggregatorConnector adds delivery-aggregator operations: pushing the catalog out
// and normalizing inbound order webhooks to events.
type AggregatorConnector interface {
	Connector
	WebhookVerifier
	// PushMenu publishes the catalog (menuJSON) to the aggregator and returns the
	// number of items accepted.
	PushMenu(ctx context.Context, menuJSON []byte) (int, error)
}

// Registry stores connectors and looks them up by id or capability.
type Registry interface {
	Register(Connector)
	Get(id string) (Connector, bool)
	ByCapability(Capability) []Connector
}
