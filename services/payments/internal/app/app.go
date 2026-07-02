// Package app holds the payments use cases. It depends only on ports + domain. It
// orchestrates provider resolution (connector-hub), provider calls (factory ->
// PaymentProvider), persistence (repo) and event emission (outbox via
// Tx.StageEvent). The grpc adapter maps proto <-> these calls; the nats consumer
// drives the webhook choreography; tests drive everything with in-memory fakes.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/domain"
	"github.com/restorna/platform/services/payments/internal/ports"
)

// Event types emitted by this service (CONVENTIONS.md naming:
// restorna.<context>.<aggregate>.<event>.v1). billing-oms consumes these to
// reconcile the bill.
const (
	EventCaptured = "restorna.payments.captured.v1"
	EventFailed   = "restorna.payments.failed.v1"
	EventRefunded = "restorna.payments.refunded.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo    ports.Repository
	hub     ports.ConnectorHub
	factory ports.ProviderFactory
	now     Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, hub ports.ConnectorHub, factory ports.ProviderFactory, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, hub: hub, factory: factory, now: now}
}

// --- CreateIntent ---

// CreateIntentInput is the validated input for creating a payment intent.
type CreateIntentInput struct {
	RestaurantID      string // trusted tenant scope from auth ctx
	BillID            string
	Amount            money.Money
	PreferConnectorID string
	IdempotencyKey    string
	CustomerContact   string
}

// CreateIntentResult carries the persisted payment plus the provider handoff params
// the customer app needs to complete payment.
type CreateIntentResult struct {
	Payment domain.Payment
	Handoff map[string]string
}

// CreateIntent is idempotent by IdempotencyKey: a repeat call returns the existing
// payment + handoff. Otherwise it resolves the active provider from connector-hub,
// instantiates the adapter, mints the provider intent, persists a CREATED->PENDING
// payment, and returns the handoff params.
func (a *App) CreateIntent(ctx context.Context, in CreateIntentInput) (CreateIntentResult, error) {
	if strings.TrimSpace(in.RestaurantID) == "" {
		return CreateIntentResult{}, fmt.Errorf("%w: restaurant scope is required", domain.ErrInvalid)
	}
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return CreateIntentResult{}, fmt.Errorf("%w: idempotency_key is required", domain.ErrInvalid)
	}

	// Idempotency: if a payment already exists for this key, return it (no new
	// provider intent, no double charge).
	if existing, err := a.repo.FindByIdempotencyKey(ctx, in.RestaurantID, in.IdempotencyKey); err == nil {
		return CreateIntentResult{Payment: existing, Handoff: handoff(existing, in.CustomerContact)}, nil
	} else if !errors.Is(err, domain.ErrNotFound) {
		return CreateIntentResult{}, err
	}

	// Resolve the active payment connector + decrypted config for this tenant.
	res, err := a.hub.ResolvePayment(ctx, in.RestaurantID, in.PreferConnectorID)
	if err != nil {
		return CreateIntentResult{}, fmt.Errorf("resolve payment connector: %w", err)
	}
	if res.ConnectorID == "" {
		return CreateIntentResult{}, fmt.Errorf("%w: no payment connector installed", domain.ErrInvalid)
	}

	// Build the domain payment (validates amount/currency/ids).
	p, err := domain.NewPayment(in.RestaurantID, in.BillID, in.Amount, res.ConnectorID, a.now())
	if err != nil {
		return CreateIntentResult{}, err
	}

	// Instantiate the provider adapter and mint the intent (external call BEFORE
	// persist so we store the real provider_ref; a failure here persists nothing).
	provider, err := a.factory.Payment(ctx, res.ConnectorID, res.Config)
	if err != nil {
		return CreateIntentResult{}, fmt.Errorf("build provider %q: %w", res.ConnectorID, err)
	}
	providerRef, err := provider.CreateIntent(ctx, p.Amount, p.ID)
	if err != nil {
		return CreateIntentResult{}, fmt.Errorf("provider create intent: %w", err)
	}
	if err := p.AttachProvider(providerRef, a.now()); err != nil {
		return CreateIntentResult{}, err
	}

	if err := a.repo.Atomic(ctx, in.RestaurantID, func(tx ports.Tx) error {
		return tx.Insert(ctx, p, in.IdempotencyKey)
	}); err != nil {
		return CreateIntentResult{}, err
	}

	return CreateIntentResult{Payment: p, Handoff: handoff(p, in.CustomerContact)}, nil
}

// --- Capture (auth+capture flows) ---

// Capture confirms an authorized intent via the provider, then flips the payment to
// CAPTURED and emits payments.captured. Idempotent on an already-captured payment.
func (a *App) Capture(ctx context.Context, restaurantID, paymentID string) (domain.Payment, error) {
	p, err := a.repo.Get(ctx, restaurantID, paymentID)
	if err != nil {
		return domain.Payment{}, err
	}
	if p.Status == domain.StatusCaptured {
		return p, nil
	}
	if !p.CanCapture() {
		return domain.Payment{}, domain.ErrInvalidState
	}

	res, err := a.hub.ResolvePayment(ctx, restaurantID, p.ConnectorID)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("resolve payment connector: %w", err)
	}
	provider, err := a.factory.Payment(ctx, p.ConnectorID, res.Config)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("build provider: %w", err)
	}
	if _, err := provider.Capture(ctx, p.ProviderRef); err != nil {
		return domain.Payment{}, fmt.Errorf("provider capture: %w", err)
	}

	var out domain.Payment
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		cur, err := tx.Get(ctx, paymentID)
		if err != nil {
			return err
		}
		if err := cur.MarkCaptured(p.Method, a.now()); err != nil {
			return err
		}
		if err := tx.Update(ctx, cur); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventCaptured, restaurantID, capturedEvent(cur)); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return domain.Payment{}, err
	}
	return out, nil
}

// --- Refund ---

// Refund issues a (full or partial) refund via the provider, records it on the
// payment, and emits payments.refunded. amount nil/zero => full remaining balance.
func (a *App) Refund(ctx context.Context, restaurantID, paymentID string, amount money.Money, reason string) (domain.Payment, error) {
	p, err := a.repo.Get(ctx, restaurantID, paymentID)
	if err != nil {
		return domain.Payment{}, err
	}
	if p.Status != domain.StatusCaptured && p.Status != domain.StatusRefunded {
		return domain.Payment{}, domain.ErrInvalidState
	}

	// Default to the full remaining (captured minus already-refunded) balance.
	if amount.Minor <= 0 {
		amount = money.New(p.Amount.Minor-p.Refunded.Minor, p.Amount.Currency)
	}
	if amount.Currency == "" {
		amount.Currency = p.Amount.Currency
	}

	res, err := a.hub.ResolvePayment(ctx, restaurantID, p.ConnectorID)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("resolve payment connector: %w", err)
	}
	provider, err := a.factory.Payment(ctx, p.ConnectorID, res.Config)
	if err != nil {
		return domain.Payment{}, fmt.Errorf("build provider: %w", err)
	}
	if err := provider.Refund(ctx, p.ProviderRef, amount); err != nil {
		return domain.Payment{}, fmt.Errorf("provider refund: %w", err)
	}

	var out domain.Payment
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		cur, err := tx.Get(ctx, paymentID)
		if err != nil {
			return err
		}
		if err := cur.ApplyRefund(amount, a.now()); err != nil {
			return err
		}
		if err := tx.Update(ctx, cur); err != nil {
			return err
		}
		if err := tx.StageEvent(ctx, EventRefunded, restaurantID, refundedEvent(cur, amount, reason)); err != nil {
			return err
		}
		out = cur
		return nil
	})
	if err != nil {
		return domain.Payment{}, err
	}
	return out, nil
}

// --- GetPayment ---

// GetPayment returns a payment by id (RLS-scoped to the caller's restaurant).
func (a *App) GetPayment(ctx context.Context, restaurantID, paymentID string) (domain.Payment, error) {
	return a.repo.Get(ctx, restaurantID, paymentID)
}

// --- Webhook choreography ---

// WebhookEvent is the normalized inbound payment event (from connector-hub's
// restorna.connector.payment.captured/failed) the consumer hands to the app.
type WebhookEvent struct {
	EventID      string // dedupe key (idempotent)
	RestaurantID string // envelope tenant (may be empty -> match by provider_ref)
	ProviderRef  string
	Captured     bool // true=captured, false=failed
	Method       string
}

// OnWebhook applies a normalized provider webhook to the matching Payment: it looks
// up by provider_ref, flips status to CAPTURED/FAILED, emits payments.captured/
// .failed, and marks the event id processed in the SAME tx (exactly-once effect).
// A redelivered event id is a no-op; an unknown provider_ref is acked (dropped)
// so a stray webhook never wedges the consumer.
func (a *App) OnWebhook(ctx context.Context, w WebhookEvent) error {
	if w.ProviderRef == "" {
		return nil
	}
	p, err := a.repo.FindByProviderRef(ctx, w.RestaurantID, w.ProviderRef)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return nil // no matching payment: drop (ack)
		}
		return err
	}
	restaurantID := p.RestaurantID

	return a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		cur, err := tx.Get(ctx, p.ID)
		if err != nil {
			return err
		}

		prior := cur.Status
		eventType := EventFailed
		var flipErr error
		if w.Captured {
			flipErr = cur.MarkCaptured(w.Method, a.now())
			eventType = EventCaptured
		} else {
			flipErr = cur.MarkFailed(a.now())
		}
		// An out-of-order webhook (e.g. failed arriving after captured) is a benign
		// no-op: record the event id as processed and drop it rather than NAK-looping
		// a poison message. Only a genuine persistence error propagates.
		if errors.Is(flipErr, domain.ErrInvalidState) {
			return tx.MarkProcessed(ctx, restaurantID, w.EventID)
		}
		if flipErr != nil {
			return flipErr
		}

		// No actual state change (idempotent redelivery of an already-terminal
		// status): don't re-emit the domain event; just mark processed.
		if cur.Status == prior {
			return tx.MarkProcessed(ctx, restaurantID, w.EventID)
		}

		if err := tx.Update(ctx, cur); err != nil {
			return err
		}
		var data any
		if w.Captured {
			data = capturedEvent(cur)
		} else {
			data = failedEvent(cur)
		}
		if err := tx.StageEvent(ctx, eventType, restaurantID, data); err != nil {
			return err
		}
		// Dedupe: mark the webhook event id processed atomically with the flip.
		return tx.MarkProcessed(ctx, restaurantID, w.EventID)
	})
}

// --- helpers ---

// handoff builds the provider handoff map the customer app needs. Real gateways
// return client keys/tokens; here we surface the ids + method the SDK uses.
func handoff(p domain.Payment, contact string) map[string]string {
	h := map[string]string{
		"payment_id":   p.ID,
		"connector_id": p.ConnectorID,
		"provider_ref": p.ProviderRef,
		"amount_minor": itoa(p.Amount.Minor),
		"currency":     p.Amount.Currency,
	}
	if contact != "" {
		h["customer_contact"] = contact
	}
	return h
}

func itoa(n int64) string { return fmt.Sprintf("%d", n) }

// event payloads (small + stable; billing-oms projects these to reconcile bills).

func capturedEvent(p domain.Payment) map[string]any {
	return map[string]any{
		"payment_id":    p.ID,
		"bill_id":       p.BillID,
		"restaurant_id": p.RestaurantID,
		"connector_id":  p.ConnectorID,
		"provider_ref":  p.ProviderRef,
		"amount_minor":  p.Amount.Minor,
		"currency":      p.Amount.Currency,
		"method":        p.Method,
		"status":        string(p.Status),
		"captured_at":   p.UpdatedAt.Format(time.RFC3339),
	}
}

func failedEvent(p domain.Payment) map[string]any {
	return map[string]any{
		"payment_id":    p.ID,
		"bill_id":       p.BillID,
		"restaurant_id": p.RestaurantID,
		"connector_id":  p.ConnectorID,
		"provider_ref":  p.ProviderRef,
		"amount_minor":  p.Amount.Minor,
		"currency":      p.Amount.Currency,
		"status":        string(p.Status),
		"failed_at":     p.UpdatedAt.Format(time.RFC3339),
	}
}

func refundedEvent(p domain.Payment, amount money.Money, reason string) map[string]any {
	return map[string]any{
		"payment_id":     p.ID,
		"bill_id":        p.BillID,
		"restaurant_id":  p.RestaurantID,
		"provider_ref":   p.ProviderRef,
		"refund_minor":   amount.Minor,
		"refunded_total": p.Refunded.Minor,
		"currency":       p.Amount.Currency,
		"reason":         reason,
		"status":         string(p.Status),
		"refunded_at":    p.UpdatedAt.Format(time.RFC3339),
	}
}
