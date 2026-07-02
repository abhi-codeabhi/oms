// Package registry adapts pkg/connectors (the connector adapter set built by the
// connectors agent) to the app's ports.Connectors interface. It exposes connector
// manifests and delegates inbound-webhook verification to the concrete connector.
//
// It codes against the pkg/connectors surface:
//
//	connectors.All() []connector.Manifest              // registered manifests
//	connectors.New(id string, cfg map[string]string) (connector.Connector, error)
//
// The concrete connector authenticates + normalizes a webhook via one of two
// VerifyWebhook shapes: aggregator + notification connectors take the raw headers
// (pkg/connector.WebhookVerifier); payment connectors predate that and take a
// single extracted signature (the local sigWebhookVerifier shape). VerifyWebhook
// below tries the headers shape first, then the signature shape.
package registry

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/connectors"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// sigWebhookVerifier is the payment-style inbound-webhook shape: the connector
// receives the raw body plus a single pre-extracted signature string. Only the
// payment connectors (razorpay/paytm/phonepe/mockpay) satisfy it — they predate
// the headers-based shape (see pkg/connector.PaymentConnector.VerifyWebhook).
type sigWebhookVerifier interface {
	VerifyWebhook(ctx context.Context, body []byte, sig string) (events.Event, error)
}

// Registry implements ports.Connectors over pkg/connectors.
type Registry struct {
	// signatureHeaders are the header keys (lowercased) checked, in order, to find
	// the provider's webhook signature. Covers the common vendors.
	signatureHeaders []string
}

var _ ports.Connectors = (*Registry)(nil)

// New builds the registry adapter.
func New() *Registry {
	return &Registry{
		signatureHeaders: []string{
			"x-razorpay-signature",
			"x-webhook-signature",
			"verify-signature",   // paytm
			"x-verify",           // phonepe
			"x-zomato-signature",
			"x-swiggy-signature",
			"signature",
		},
	}
}

// All returns the manifests of every registered connector (unfiltered).
func (r *Registry) All() []ports.ConnectorManifest {
	mans := connectors.All()
	out := make([]ports.ConnectorManifest, 0, len(mans))
	for _, m := range mans {
		out = append(out, toPortManifest(m))
	}
	return out
}

// Get returns a single connector's manifest by id.
func (r *Registry) Get(id string) (ports.ConnectorManifest, bool) {
	for _, m := range connectors.All() {
		if m.ID == id {
			return toPortManifest(m), true
		}
	}
	return ports.ConnectorManifest{}, false
}

// VerifyWebhook builds the connector with the tenant's decrypted config, extracts
// the signature from the headers, and asks the connector to authenticate +
// normalize the payload. A verification failure (bad/forged signature) surfaces as
// an error / verified=false so the app rejects the webhook.
func (r *Registry) VerifyWebhook(ctx context.Context, connectorID string, cfg map[string]string, body []byte, headers map[string]string) (ports.Webhook, error) {
	c, err := connectors.New(connectorID, cfg)
	if err != nil {
		return ports.Webhook{}, fmt.Errorf("%w: build connector %q: %v", domain.ErrInvalid, connectorID, err)
	}
	// Connectors implement one of two VerifyWebhook shapes. Aggregator +
	// notification connectors take the raw headers (connector.WebhookVerifier);
	// payment connectors predate that and take a single extracted signature. Try
	// the headers shape first, then fall back to the signature shape.
	if hv, ok := c.(connector.WebhookVerifier); ok {
		ev, err := hv.VerifyWebhook(ctx, body, headers)
		if err != nil {
			return ports.Webhook{Verified: false}, err
		}
		return ports.Webhook{Event: ev, Verified: true}, nil
	}
	if sv, ok := c.(sigWebhookVerifier); ok {
		sig := r.extractSignature(headers)
		ev, err := sv.VerifyWebhook(ctx, body, sig)
		if err != nil {
			// Verification failed (tampered/forged signature) — reject.
			return ports.Webhook{Verified: false}, err
		}
		return ports.Webhook{Event: ev, Verified: true}, nil
	}
	return ports.Webhook{}, fmt.Errorf("%w: connector %q does not accept webhooks", domain.ErrInvalid, connectorID)
}

// extractSignature finds the provider signature in the (case-insensitive) headers.
func (r *Registry) extractSignature(headers map[string]string) string {
	lower := make(map[string]string, len(headers))
	for k, v := range headers {
		lower[toLower(k)] = v
	}
	for _, h := range r.signatureHeaders {
		if v, ok := lower[h]; ok && v != "" {
			return v
		}
	}
	return ""
}

func toLower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

// toPortManifest converts a pkg/connector.Manifest into the app-facing view,
// deriving the secret-key set from the config schema.
func toPortManifest(m connector.Manifest) ports.ConnectorManifest {
	caps := make([]domain.Capability, 0, len(m.Capabilities))
	for _, c := range m.Capabilities {
		caps = append(caps, domain.Capability(c))
	}
	return ports.ConnectorManifest{
		ID:           m.ID,
		Name:         m.Name,
		Capabilities: caps,
		ConfigSchema: []byte(m.ConfigSchema),
		LogoURL:      logoURLFrom(m),
		SecretKeys:   secretKeysFrom(m.ConfigSchema),
	}
}

// logoURLFrom reads a "logo_url" hint from the schema if present (the SDK manifest
// has no dedicated logo field; connectors may embed it in the schema metadata).
func logoURLFrom(m connector.Manifest) string {
	var meta struct {
		LogoURL string `json:"logo_url"`
	}
	if len(m.ConfigSchema) > 0 {
		_ = json.Unmarshal(m.ConfigSchema, &meta)
	}
	return meta.LogoURL
}

// secretKeysFrom parses the connector's JSON config schema for keys flagged secret.
// Two shapes are supported:
//
//	{"fields":[{"key":"api_key","secret":true}, ...]}
//	{"properties":{"api_key":{"secret":true}, ...}}
func secretKeysFrom(schema json.RawMessage) map[string]bool {
	out := map[string]bool{}
	if len(schema) == 0 {
		return out
	}
	var byFields struct {
		Fields []struct {
			Key    string `json:"key"`
			Secret bool   `json:"secret"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(schema, &byFields); err == nil && len(byFields.Fields) > 0 {
		for _, f := range byFields.Fields {
			if f.Secret {
				out[f.Key] = true
			}
		}
		return out
	}
	var byProps struct {
		Properties map[string]struct {
			Secret bool `json:"secret"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(schema, &byProps); err == nil {
		for k, v := range byProps.Properties {
			if v.Secret {
				out[k] = true
			}
		}
	}
	return out
}
