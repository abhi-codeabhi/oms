// Package app holds the connector-hub use cases. It depends only on ports +
// domain. It orchestrates entitlement filtering + quota reservation, persistence
// (repo), envelope encryption (Crypto), connector lookup + webhook verification
// (Connectors), and event publication (EventBus). The grpc adapter maps proto <->
// these calls; tests drive it with in-memory fakes.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// Event types emitted by this service (see CONVENTIONS.md naming). Inbound
// provider webhooks are normalized by the connector into a CloudEvent whose type
// the connector supplies; the hub also emits a lifecycle event on install.
const EventConnectorInstalled = "restorna.connector.installation.created.v1"

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo   ports.Repository
	ents   ports.Entitlements
	crypto ports.Crypto
	conns  ports.Connectors
	bus    ports.EventBus
	now    Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, ents ports.Entitlements, crypto ports.Crypto, conns ports.Connectors, bus ports.EventBus, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, ents: ents, crypto: crypto, conns: conns, bus: bus, now: now}
}

// --- ListAvailable ---

// ListAvailable returns the connector marketplace FILTERED by the owner's
// entitlements: a connector is shown only if at least one of its capabilities is
// unlocked by the owner's plan (capability feature flag). This is what makes plans
// gate which integrations an owner can even see.
func (a *App) ListAvailable(ctx context.Context, ownerID string) ([]ports.ConnectorManifest, error) {
	if ownerID == "" {
		return nil, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	all := a.conns.All()
	out := make([]ports.ConnectorManifest, 0, len(all))
	// Cache feature lookups per capability to avoid duplicate RPCs.
	featureCache := map[domain.Capability]bool{}
	for _, m := range all {
		allowed, err := a.manifestAllowed(ctx, ownerID, m, featureCache)
		if err != nil {
			return nil, err
		}
		if allowed {
			out = append(out, m)
		}
	}
	return out, nil
}

// manifestAllowed reports whether any capability of m is unlocked for the owner.
func (a *App) manifestAllowed(ctx context.Context, ownerID string, m ports.ConnectorManifest, cache map[domain.Capability]bool) (bool, error) {
	for _, c := range m.Capabilities {
		if v, ok := cache[c]; ok {
			if v {
				return true, nil
			}
			continue
		}
		feature := domain.FeatureForCapability(c)
		if feature == "" {
			cache[c] = false
			continue
		}
		enabled, err := a.ents.HasFeature(ctx, ownerID, feature)
		if err != nil {
			return false, fmt.Errorf("check feature %q: %w", feature, err)
		}
		cache[c] = enabled
		if enabled {
			return true, nil
		}
	}
	return false, nil
}

// --- Install ---

// InstallInput is the validated input for installing a connector.
type InstallInput struct {
	OwnerID      string
	RestaurantID string
	ConnectorID  string
	TestMode     bool
	Config       map[string]string // incl. secrets (stored encrypted)
}

// Install reserves the "connectors" quota, encrypts the secret config values with
// the Crypto port (envelope encryption), and stores one Installation for the
// tenant. On any failure after a successful reservation the quota is released
// (compensation). Secrets never persist in plaintext.
func (a *App) Install(ctx context.Context, in InstallInput) (domain.Installation, error) {
	if in.OwnerID == "" {
		return domain.Installation{}, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	man, ok := a.conns.Get(in.ConnectorID)
	if !ok {
		return domain.Installation{}, fmt.Errorf("%w: unknown connector %q", domain.ErrInvalid, in.ConnectorID)
	}

	// Gate: at least one capability must be unlocked by the owner's plan.
	allowed, err := a.manifestAllowed(ctx, in.OwnerID, man, map[domain.Capability]bool{})
	if err != nil {
		return domain.Installation{}, err
	}
	if !allowed {
		return domain.Installation{}, fmt.Errorf("%w: %s", domain.ErrForbidden, in.ConnectorID)
	}

	// Split the config into public (plaintext) + secret (encrypted) parts.
	public, secret := man.ConfigSpec().SplitConfig(in.Config)
	cipher, err := a.crypto.EncryptMap(secret)
	if err != nil {
		return domain.Installation{}, fmt.Errorf("encrypt secret config: %w", err)
	}

	inst, err := domain.NewInstallation(in.OwnerID, in.RestaurantID, in.ConnectorID, in.TestMode, public, cipher, a.now())
	if err != nil {
		return domain.Installation{}, err
	}

	// Quota gate: reserve one "connectors" slot, idempotent by the new install id.
	res, err := a.ents.ReserveQuota(ctx, in.OwnerID, domain.QuotaConnectors, 1, inst.ID)
	if err != nil {
		return domain.Installation{}, fmt.Errorf("reserve connectors quota: %w", err)
	}
	if !res.OK {
		return domain.Installation{}, quotaErr(domain.QuotaConnectors, res.UpgradeHint)
	}

	err = a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
		exists, err := tx.ExistsForConnector(ctx, in.OwnerID, in.RestaurantID, in.ConnectorID)
		if err != nil {
			return err
		}
		if exists {
			return fmt.Errorf("%w: %s already installed", domain.ErrAlreadyExists, in.ConnectorID)
		}
		if err := tx.InsertInstallation(ctx, inst); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventConnectorInstalled, inst.OwnerID, installEvent(inst))
	})
	if err != nil {
		// Compensation: release the reserved slot on any persistence failure.
		_ = a.ents.ReleaseQuota(ctx, in.OwnerID, domain.QuotaConnectors, 1, inst.ID)
		return domain.Installation{}, err
	}
	return inst, nil
}

// --- UpdateInstallation ---

// UpdateInput is the validated input for updating an installation.
type UpdateInput struct {
	OwnerID        string
	InstallationID string
	Enabled        bool
	Config         map[string]string // full config; secrets re-encrypted if present
}

// UpdateInstallation toggles enabled and merges config. Any secret keys present in
// the incoming config are re-encrypted and replace the stored ciphertext; if no
// secret keys are supplied the existing secrets are preserved untouched.
func (a *App) UpdateInstallation(ctx context.Context, in UpdateInput) (domain.Installation, error) {
	if !ids.Valid(domain.PrefixInstallation, in.InstallationID) {
		return domain.Installation{}, fmt.Errorf("%w: installation_id is invalid", domain.ErrInvalid)
	}
	var out domain.Installation
	err := a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
		cur, err := tx.GetInstallation(ctx, in.InstallationID)
		if err != nil {
			return err
		}
		man, ok := a.conns.Get(cur.ConnectorID)
		if !ok {
			return fmt.Errorf("%w: unknown connector %q", domain.ErrInvalid, cur.ConnectorID)
		}
		public, secret := man.ConfigSpec().SplitConfig(in.Config)

		var cipher []byte
		if len(secret) > 0 {
			// Merge new secrets over the existing ones, then re-encrypt the whole set.
			existing, derr := a.crypto.DecryptMap(cur.SecretConfig)
			if derr != nil {
				return derr
			}
			for k, v := range secret {
				existing[k] = v
			}
			cipher, err = a.crypto.EncryptMap(existing)
			if err != nil {
				return fmt.Errorf("re-encrypt secret config: %w", err)
			}
		}
		// public may be empty when only secrets/enabled changed; pass nil to keep.
		var pub map[string]string
		if len(in.Config) > 0 {
			pub = mergePublic(cur.PublicConfig, public)
		}
		cur.ApplyUpdate(in.Enabled, pub, cipher, a.now())
		if err := tx.UpdateInstallation(ctx, cur); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return domain.Installation{}, err
	}
	return out, nil
}

// --- ListInstallations ---

// ListInstallations returns the owner's installations, optionally filtered by a
// capability. Secrets are NEVER included (the grpc adapter echoes only public
// config). If capability != "" only installations whose connector declares it are
// returned.
func (a *App) ListInstallations(ctx context.Context, ownerID string, capability domain.Capability) ([]domain.Installation, error) {
	if ownerID == "" {
		return nil, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	all, err := a.repo.ListInstallations(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	if capability == "" {
		return all, nil
	}
	out := make([]domain.Installation, 0, len(all))
	for _, inst := range all {
		if man, ok := a.conns.Get(inst.ConnectorID); ok && hasCapability(man, capability) {
			out = append(out, inst)
		}
	}
	return out, nil
}

// --- Resolve ---

// Resolved is the internal answer for "which connector should serve this
// capability at this tenant, and with what (decrypted) config".
type Resolved struct {
	ConnectorID    string
	InstallationID string
	TestMode       bool
	Config         map[string]string // merged public + DECRYPTED secret config
}

// Resolve picks the active (enabled) installation for a capability at the owner,
// preferring preferConnectorID when supplied, and returns its connector id plus
// the fully DECRYPTED config. Used internally by payments/aggregators/
// notifications. Returns ErrNotFound when no enabled connector serves the
// capability.
func (a *App) Resolve(ctx context.Context, ownerID string, capability domain.Capability, preferConnectorID string) (Resolved, error) {
	if ownerID == "" {
		return Resolved{}, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	all, err := a.repo.ListByOwner(ctx, ownerID)
	if err != nil {
		return Resolved{}, err
	}

	var chosen *domain.Installation
	for i := range all {
		inst := all[i]
		if !inst.Enabled {
			continue
		}
		man, ok := a.conns.Get(inst.ConnectorID)
		if !ok || !hasCapability(man, capability) {
			continue
		}
		if preferConnectorID != "" && inst.ConnectorID == preferConnectorID {
			chosen = &all[i]
			break
		}
		if chosen == nil {
			chosen = &all[i]
		}
	}
	if chosen == nil {
		return Resolved{}, fmt.Errorf("%w: no enabled connector for capability %q", domain.ErrNotFound, capability)
	}

	secret, err := a.crypto.DecryptMap(chosen.SecretConfig)
	if err != nil {
		return Resolved{}, err
	}
	cfg := mergePublic(chosen.PublicConfig, secret)
	return Resolved{
		ConnectorID:    chosen.ConnectorID,
		InstallationID: chosen.ID,
		TestMode:       chosen.TestMode,
		Config:         cfg,
	}, nil
}

// --- IngestWebhook ---

// WebhookInput is the validated input for an inbound provider webhook.
type WebhookInput struct {
	// OwnerID scopes the lookup of the installation whose credentials verify the
	// signature. For a public webhook edge the gateway resolves it from the route /
	// path; here it is provided by the caller.
	OwnerID     string
	ConnectorID string
	Body        []byte
	Headers     map[string]string
}

// WebhookResult is the outcome of ingesting a webhook.
type WebhookResult struct {
	EventType string
	Accepted  bool
}

// IngestWebhook looks up the connector's installation for the owner, decrypts its
// config, and hands the raw body + headers to the connector's VerifyWebhook to
// authenticate (reject tampered/forged payloads) + normalize into a CloudEvent.
// On success it publishes the event to NATS and returns the event type. A failed
// verification returns ErrWebhookUnverified and publishes nothing.
func (a *App) IngestWebhook(ctx context.Context, in WebhookInput) (WebhookResult, error) {
	if in.ConnectorID == "" {
		return WebhookResult{}, fmt.Errorf("%w: connector_id is required", domain.ErrInvalid)
	}

	// Resolve the tenant's decrypted config so the connector can check the signature.
	cfg := map[string]string{}
	if in.OwnerID != "" {
		all, err := a.repo.ListByOwner(ctx, in.OwnerID)
		if err != nil {
			return WebhookResult{}, err
		}
		for _, inst := range all {
			if inst.ConnectorID == in.ConnectorID && inst.Enabled {
				secret, derr := a.crypto.DecryptMap(inst.SecretConfig)
				if derr != nil {
					return WebhookResult{}, derr
				}
				cfg = mergePublic(inst.PublicConfig, secret)
				break
			}
		}
	}

	wh, err := a.conns.VerifyWebhook(ctx, in.ConnectorID, cfg, in.Body, in.Headers)
	if err != nil {
		return WebhookResult{}, fmt.Errorf("%w: %v", domain.ErrWebhookUnverified, err)
	}
	if !wh.Verified {
		return WebhookResult{Accepted: false}, domain.ErrWebhookUnverified
	}

	if err := a.bus.Publish(ctx, wh.Event); err != nil {
		return WebhookResult{}, fmt.Errorf("publish webhook event: %w", err)
	}
	return WebhookResult{EventType: wh.Event.Type, Accepted: true}, nil
}

// --- helpers ---

func hasCapability(m ports.ConnectorManifest, c domain.Capability) bool {
	for _, have := range m.Capabilities {
		if have == c {
			return true
		}
	}
	return false
}

// mergePublic returns a new map = base overlaid with overlay (overlay wins).
func mergePublic(base, overlay map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(overlay))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range overlay {
		out[k] = v
	}
	return out
}

// quotaErr wraps ErrQuotaExceeded carrying the upgrade hint; the grpc adapter
// surfaces it as ResourceExhausted with the hint in the message.
func quotaErr(key, hint string) error {
	msg := fmt.Sprintf("%s quota reached", key)
	if hint != "" {
		msg += ": " + hint
	}
	return &QuotaError{Key: key, Hint: hint, msg: msg}
}

// QuotaError is a typed quota failure; errors.Is(err, domain.ErrQuotaExceeded) is true.
type QuotaError struct {
	Key  string
	Hint string
	msg  string
}

func (e *QuotaError) Error() string { return e.msg }
func (e *QuotaError) Is(target error) bool {
	return errors.Is(target, domain.ErrQuotaExceeded)
}

// installEvent is the small, stable payload projected by consumers.
func installEvent(i domain.Installation) map[string]any {
	return map[string]any{
		"installation_id": i.ID,
		"owner_id":        i.OwnerID,
		"restaurant_id":   i.RestaurantID,
		"connector_id":    i.ConnectorID,
		"test_mode":       i.TestMode,
		"installed_at":    i.InstalledAt.Format(time.RFC3339),
	}
}
