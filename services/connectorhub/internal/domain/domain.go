// Package domain holds the pure connector-hub model: an Installation (a connector
// configured for a tenant scope) plus capability rules. It imports NO
// infrastructure (no pgx, nats, connect). Rules live here; adapters map this to and
// from proto and SQL.
package domain

import (
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// ID type prefix for installations (see CONVENTIONS.md: type-prefixed ULIDs).
const PrefixInstallation = "inst"

// Quota / feature keys checked against the EntitlementsService. A plan unlocks
// which connectors are available (feature flag keyed by capability, e.g.
// "aggregators") plus a numeric "connectors" install quota.
const (
	// QuotaConnectors is the numeric limit on how many connectors an owner may
	// install (Plan.quotas["connectors"], -1 = unlimited).
	QuotaConnectors = "connectors"
)

// Capability tags what a connector can do. Mirrors pkg/connector.Capability and
// the proto Capability enum; kept as a local string type so the domain owns its
// rules without importing the SDK.
type Capability string

const (
	CapabilityPayment      Capability = "payment"
	CapabilityAggregator   Capability = "aggregator"
	CapabilityCRM          Capability = "crm"
	CapabilityERP          Capability = "erp"
	CapabilityNotification Capability = "notification"
)

// FeatureForCapability maps a capability to the boolean entitlement feature flag
// that unlocks connectors of that kind. ListAvailable filters the marketplace by
// this flag (owners only see connectors their plan permits).
func FeatureForCapability(c Capability) string {
	switch c {
	case CapabilityPayment:
		return "payments"
	case CapabilityAggregator:
		return "aggregators"
	case CapabilityCRM:
		return "crm"
	case CapabilityERP:
		return "erp"
	case CapabilityNotification:
		return "notifications"
	default:
		return ""
	}
}

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid       = errors.New("invalid argument")
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrQuotaExceeded = errors.New("quota exceeded")
	// ErrForbidden is returned when the owner's plan does not permit a connector
	// (capability feature flag disabled).
	ErrForbidden = errors.New("connector not permitted by plan")
	// ErrWebhookUnverified is returned when a connector cannot authenticate an
	// inbound webhook (bad/missing signature). Mapped to PermissionDenied.
	ErrWebhookUnverified = errors.New("webhook signature verification failed")
)

// Installation is a connector configured for a tenant scope (owner + optional
// restaurant). Its secret configuration is held ENCRYPTED at rest: the domain only
// ever carries the ciphertext (SecretConfig) plus the non-secret keys echoed back
// to clients (PublicConfig). Decryption happens in the app via the Crypto port and
// the plaintext never persists.
type Installation struct {
	ID           string
	OwnerID      string
	RestaurantID string // "" = owner-level (brand-wide) installation
	ConnectorID  string
	Enabled      bool
	TestMode     bool
	// PublicConfig holds the non-secret config keys (echoed back on reads).
	PublicConfig map[string]string
	// SecretConfig is the AES-GCM ciphertext (envelope-encrypted) of the full
	// config map's secret keys, marshalled as JSON before encryption. Never echoed.
	SecretConfig []byte
	InstalledAt  time.Time
	UpdatedAt    time.Time
}

// ConfigSpec describes, per connector, which config keys are secret so the app can
// split an incoming config map into public (stored plaintext) and secret
// (encrypted) parts. It is derived from the connector's manifest config schema.
type ConfigSpec struct {
	// Secret is the set of config keys whose values must be encrypted at rest and
	// never echoed back to clients.
	Secret map[string]bool
}

// IsSecret reports whether key is a secret field per the spec.
func (s ConfigSpec) IsSecret(key string) bool { return s.Secret[key] }

// SplitConfig partitions cfg into (public, secret) maps using the spec. Any key
// not marked secret is treated as public.
func (s ConfigSpec) SplitConfig(cfg map[string]string) (public, secret map[string]string) {
	public = map[string]string{}
	secret = map[string]string{}
	for k, v := range cfg {
		if s.IsSecret(k) {
			secret[k] = v
		} else {
			public[k] = v
		}
	}
	return public, secret
}

// NewInstallation constructs and validates a new Installation. connectorID is
// required; ownerID is the tenant root (from the auth scope, never the body).
// public/secret ciphertext are attached by the app after encryption.
func NewInstallation(ownerID, restaurantID, connectorID string, testMode bool, public map[string]string, secretCipher []byte, now time.Time) (Installation, error) {
	ownerID = strings.TrimSpace(ownerID)
	connectorID = strings.TrimSpace(connectorID)
	if ownerID == "" {
		return Installation{}, fieldErr("owner_id is required")
	}
	if connectorID == "" {
		return Installation{}, fieldErr("connector_id is required")
	}
	if public == nil {
		public = map[string]string{}
	}
	return Installation{
		ID:           ids.New(PrefixInstallation),
		OwnerID:      ownerID,
		RestaurantID: strings.TrimSpace(restaurantID),
		ConnectorID:  connectorID,
		Enabled:      true, // installed connectors start enabled
		TestMode:     testMode,
		PublicConfig: public,
		SecretConfig: secretCipher,
		InstalledAt:  now.UTC(),
		UpdatedAt:    now.UTC(),
	}, nil
}

// ApplyUpdate mutates enabled and merges the supplied public config + secret
// ciphertext (a fresh re-encryption of the merged secret map done by the app). A
// nil secretCipher means "secrets unchanged".
func (i *Installation) ApplyUpdate(enabled bool, public map[string]string, secretCipher []byte, now time.Time) {
	i.Enabled = enabled
	if public != nil {
		i.PublicConfig = public
	}
	if secretCipher != nil {
		i.SecretConfig = secretCipher
	}
	i.UpdatedAt = now.UTC()
}

// PublicKeys returns the sorted non-secret config keys (deterministic for tests).
func (i Installation) PublicKeys() []string {
	out := make([]string, 0, len(i.PublicConfig))
	for k := range i.PublicConfig {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
