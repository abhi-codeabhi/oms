// Package app holds the notifications use cases. It depends only on ports + domain.
// It orchestrates template rendering (domain), provider resolution (connector-hub),
// adapter instantiation (provider factory), sending, and persistence (repo +
// outbox). The grpc adapter maps proto <-> these calls; tests drive it with
// in-memory fakes.
package app

import (
	"context"
	"fmt"
	"time"

	"github.com/restorna/platform/services/notifications/internal/domain"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

// Event types emitted by this service (see CONVENTIONS.md naming).
const (
	EventMessageSent    = "restorna.notifications.message.sent.v1"
	EventMessageFailed  = "restorna.notifications.message.failed.v1"
	EventMessageUpdated = "restorna.notifications.message.updated.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo     ports.Repository
	hub      ports.ConnectorHub
	provider ports.ProviderFactory
	now      Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, hub ports.ConnectorHub, provider ports.ProviderFactory, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, hub: hub, provider: provider, now: now}
}

// SendInput is the validated input for sending a notification.
type SendInput struct {
	OwnerID        string
	RestaurantID   string
	Channel        domain.Channel
	To             string
	TemplateID     string
	Vars           map[string]string
	IdempotencyKey string
}

// Send renders the template, resolves the tenant's notification provider (falling
// back to the built-in lognotify mock when none is installed), dispatches the
// message, and persists it QUEUED -> SENT with the provider reference. It is
// idempotent by IdempotencyKey: a repeated key returns the prior message unchanged
// (no second dispatch), so retries never double-send an OTP.
func (a *App) Send(ctx context.Context, in SendInput) (domain.Message, error) {
	if in.OwnerID == "" {
		return domain.Message{}, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	if !in.Channel.Valid() {
		return domain.Message{}, fmt.Errorf("%w: channel is invalid", domain.ErrInvalid)
	}

	// Idempotency: short-circuit on a prior message for this key.
	if in.IdempotencyKey != "" {
		if prior, ok, err := a.repo.FindByIdempotencyKey(ctx, in.OwnerID, in.IdempotencyKey); err != nil {
			return domain.Message{}, err
		} else if ok {
			return prior, nil
		}
	}

	// Render the template with the supplied vars.
	tmpl, err := a.repo.GetTemplate(ctx, in.OwnerID, in.TemplateID)
	if err != nil {
		return domain.Message{}, err
	}
	subject, body := tmpl.Render(in.Vars)

	msg, err := domain.NewMessage(in.OwnerID, in.RestaurantID, in.Channel, in.To, in.TemplateID, in.Vars, subject, body, in.IdempotencyKey, a.now())
	if err != nil {
		return domain.Message{}, err
	}

	// Resolve the active provider for CAPABILITY_NOTIFICATION at this tenant.
	sender, connectorID, err := a.resolveSender(ctx, in.OwnerID)
	if err != nil {
		return domain.Message{}, err
	}

	// Persist QUEUED first so a crash mid-send leaves a durable record.
	if err := a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
		return tx.InsertMessage(ctx, msg)
	}); err != nil {
		return domain.Message{}, err
	}

	// Dispatch to the provider. On failure, mark FAILED + emit and return the error.
	providerRef, sendErr := sender.Send(ctx, string(in.Channel), in.To, subject, body)
	if sendErr != nil {
		msg.MarkFailed(sendErr.Error(), a.now())
		_ = a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
			if err := tx.UpdateMessage(ctx, msg); err != nil {
				return err
			}
			return tx.StageEvent(ctx, EventMessageFailed, msg.OwnerID, messageEvent(msg))
		})
		return domain.Message{}, fmt.Errorf("send via %s: %w", connectorID, sendErr)
	}

	// Success: QUEUED -> SENT with the provider ref; emit message.sent.
	msg.MarkSent(connectorID, providerRef, a.now())
	if err := a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
		if err := tx.UpdateMessage(ctx, msg); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventMessageSent, msg.OwnerID, messageEvent(msg))
	}); err != nil {
		return domain.Message{}, err
	}
	return msg, nil
}

// resolveSender asks connector-hub for the tenant's notification provider and builds
// its adapter. When no provider is installed (or the build fails), it falls back to
// the built-in lognotify mock so identity OTP / staff invites still work in dev.
func (a *App) resolveSender(ctx context.Context, ownerID string) (ports.NotificationSender, string, error) {
	res, err := a.hub.Resolve(ctx, ownerID)
	if err != nil {
		return nil, "", fmt.Errorf("resolve notification provider: %w", err)
	}
	if !res.Installed || res.ConnectorID == "" {
		s, id := a.provider.Fallback(ctx)
		return s, id, nil
	}
	sender, err := a.provider.New(ctx, res.ConnectorID, res.Config)
	if err != nil {
		// A misconfigured provider must not silently drop OTP; fall back to the mock.
		s, id := a.provider.Fallback(ctx)
		return s, id, nil
	}
	return sender, res.ConnectorID, nil
}

// GetStatus returns a message by id (RLS-scoped to the caller's owner).
func (a *App) GetStatus(ctx context.Context, ownerID, messageID string) (domain.Message, error) {
	if messageID == "" {
		return domain.Message{}, fmt.Errorf("%w: message_id is required", domain.ErrInvalid)
	}
	return a.repo.GetMessage(ctx, ownerID, messageID)
}

// UpsertTemplateInput is the validated input for creating/replacing template copy.
type UpsertTemplateInput struct {
	OwnerID string
	ID      string
	Channel domain.Channel
	Subject string
	Body    string
}

// UpsertTemplate creates or replaces an owner's template copy for a channel.
func (a *App) UpsertTemplate(ctx context.Context, in UpsertTemplateInput) (domain.Template, error) {
	t, err := domain.NewTemplate(in.OwnerID, in.ID, in.Channel, in.Subject, in.Body, a.now())
	if err != nil {
		return domain.Template{}, err
	}
	if err := a.repo.UpsertTemplate(ctx, t); err != nil {
		return domain.Template{}, err
	}
	return t, nil
}

// ListTemplates returns the owner's configured templates.
func (a *App) ListTemplates(ctx context.Context, ownerID string) ([]domain.Template, error) {
	if ownerID == "" {
		return nil, fmt.Errorf("%w: owner_id is required", domain.ErrInvalid)
	}
	return a.repo.ListTemplates(ctx, ownerID)
}

// ApplyDeliveryStatus handles a provider delivery-status webhook (ingested via
// connector-hub events): it locates the message by provider id + ref and advances
// its status (QUEUED/SENT -> DELIVERED/FAILED), ignoring out-of-order regressions.
// eventID dedupes so the same webhook applied twice is a no-op (exactly-once).
func (a *App) ApplyDeliveryStatus(ctx context.Context, eventID, providerID, providerRef string, status domain.DeliveryStatus) error {
	if eventID != "" {
		isNew, err := a.repo.MarkEventProcessed(ctx, eventID)
		if err != nil {
			return err
		}
		if !isNew {
			return nil // already handled
		}
	}
	msg, ok, err := a.repo.FindByProviderRef(ctx, providerID, providerRef)
	if err != nil {
		return err
	}
	if !ok {
		return nil // unknown ref (e.g. a message from before this deploy) — ignore
	}
	if !msg.ApplyDeliveryStatus(status, a.now()) {
		return nil // no forward transition
	}
	return a.repo.Atomic(ctx, msg.OwnerID, func(tx ports.Tx) error {
		if err := tx.UpdateMessage(ctx, msg); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventMessageUpdated, msg.OwnerID, messageEvent(msg))
	})
}

// messageEvent is the small, stable event payload consumers project.
func messageEvent(m domain.Message) map[string]any {
	return map[string]any{
		"message_id":    m.ID,
		"owner_id":      m.OwnerID,
		"restaurant_id": m.RestaurantID,
		"channel":       string(m.Channel),
		"to":            m.To,
		"template":      m.TemplateID,
		"status":        string(m.Status),
		"provider_id":   m.ProviderID,
		"provider_ref":  m.ProviderRef,
		"created_at":    m.CreatedAt.Format(time.RFC3339),
	}
}
