// Package app holds the service-requests use cases. It depends only on ports +
// domain. It orchestrates persistence (repo), event emission (outbox via
// Tx.StageEvent) and threshold resolution (SettingsResolver). The grpc adapter
// maps proto <-> these calls; tests drive everything with in-memory fakes.
//
// Use cases (ported from the proven Node service-requests):
//   - Raise        : create a request, rejected FailedPrecondition if the same
//                    table+type was acknowledged within the cooldown window.
//   - ListOpen     : every request not yet done (assigned + escalated).
//   - Acknowledge  : mark done, record the table+type cooldown.
//   - EscalateDue  : flip assigned -> escalated past the escalation threshold.
package app

import (
	"context"
	"time"

	"github.com/restorna/platform/services/servicerequests/internal/domain"
	"github.com/restorna/platform/services/servicerequests/internal/ports"
)

// Event types emitted by this service (CONVENTIONS.md naming:
// restorna.<context>.<aggregate>.<event>.v1).
const (
	// EventRaised fires on every accepted Raise. billing consumes this to mark a
	// table "asked" when the request type is bill.
	EventRaised = "restorna.servicerequests.raised.v1"
	// EventEscalated fires for each request flipped to escalated by EscalateDue.
	EventEscalated = "restorna.servicerequests.escalated.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo     ports.Repository
	settings ports.SettingsResolver
	now      Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
// settings may be nil — thresholds then fall back to the package defaults (60s
// cooldown / 30s escalation), keeping the service usable when settings is down.
func New(repo ports.Repository, settings ports.SettingsResolver, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, settings: settings, now: now}
}

// RaiseInput is the validated input for raising a request.
type RaiseInput struct {
	Type       string
	Table      int32
	AssignedTo string
}

// Raise creates a request for the table. It is rejected with domain.ErrCooldown
// (mapped to FailedPrecondition by the grpc adapter) if the SAME table+type was
// acknowledged within the cooldown window resolved from settings. On success the
// request is persisted and restorna.servicerequests.raised.v1 is staged in the
// same tx (so billing can mark the table "asked" on a bill request).
func (a *App) Raise(ctx context.Context, restaurantID string, in RaiseInput) (domain.Request, error) {
	typ := domain.Type(in.Type)
	if err := domain.ValidateRaise(typ, in.Table); err != nil {
		return domain.Request{}, err
	}

	cooldown := a.thresholds(ctx, restaurantID).Cooldown
	now := a.now()

	// Read the last acknowledge time for this table+type OUTSIDE the write tx; the
	// cooldown check is a read-only gate before we attempt to create.
	lastAck, err := a.repo.LastAck(ctx, restaurantID, in.Table, typ)
	if err != nil {
		return domain.Request{}, err
	}
	if !domain.CanRaise(lastAck, now, cooldown) {
		return domain.Request{}, domain.ErrCooldown
	}

	r, err := domain.Raise(typ, in.Table, in.AssignedTo, now)
	if err != nil {
		return domain.Request{}, err
	}

	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		if err := tx.Insert(ctx, r); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventRaised, restaurantID, raisedEvent(r))
	})
	if err != nil {
		return domain.Request{}, err
	}
	return r, nil
}

// ListOpen returns every outstanding request (state != done): assigned +
// escalated, both still needing a waiter's attention.
func (a *App) ListOpen(ctx context.Context, restaurantID string) ([]domain.Request, error) {
	all, err := a.repo.List(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	return domain.OpenOnly(all), nil
}

// Acknowledge marks one request done and records the table+type cooldown so the
// guest cannot immediately re-raise the same request. No event is emitted on ack
// (the cooldown is the side effect); the raised/escalated stream is what other
// services consume.
func (a *App) Acknowledge(ctx context.Context, restaurantID, requestID string) (domain.Request, error) {
	now := a.now()
	var out domain.Request
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		r, err := tx.Get(ctx, requestID)
		if err != nil {
			return err
		}
		r.Acknowledge(now)
		if err := tx.Update(ctx, r); err != nil {
			return err
		}
		if err := tx.SetLastAck(ctx, restaurantID, r.Table, r.Type, now); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return domain.Request{}, err
	}
	return out, nil
}

// EscalateDue flips every assigned request past the escalation threshold to
// 'escalated', staging restorna.servicerequests.escalated.v1 for each, and
// returns the list that was escalated. `now` is supplied by the caller (the
// EscalateDue RPC / a scheduler) so the sweep is deterministic; when zero we fall
// back to the app clock.
func (a *App) EscalateDue(ctx context.Context, restaurantID string, now time.Time) ([]domain.Request, error) {
	if now.IsZero() {
		now = a.now()
	}
	threshold := a.thresholds(ctx, restaurantID).Escalation

	all, err := a.repo.List(ctx, restaurantID)
	if err != nil {
		return nil, err
	}

	var escalated []domain.Request
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		for _, r := range all {
			if !domain.ShouldEscalate(r, now, threshold) {
				continue
			}
			r.Escalate()
			if err := tx.Update(ctx, r); err != nil {
				return err
			}
			if err := tx.StageEvent(ctx, EventEscalated, restaurantID, escalatedEvent(r)); err != nil {
				return err
			}
			escalated = append(escalated, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return escalated, nil
}

// thresholds resolves the cooldown + escalation windows from settings, degrading
// to the package defaults when settings is unavailable (nil resolver or error).
func (a *App) thresholds(ctx context.Context, restaurantID string) ports.Thresholds {
	def := ports.Thresholds{Cooldown: ports.DefaultCooldown, Escalation: ports.DefaultEscalation}
	if a.settings == nil {
		return def
	}
	t, err := a.settings.Thresholds(ctx, restaurantID)
	if err != nil {
		return def
	}
	if t.Cooldown <= 0 {
		t.Cooldown = def.Cooldown
	}
	if t.Escalation <= 0 {
		t.Escalation = def.Escalation
	}
	return t
}

// --- event payloads (kept small + stable; consumers project these) ---

// raisedEvent carries enough for billing to mark a table "asked" on type=bill.
func raisedEvent(r domain.Request) map[string]any {
	return map[string]any{
		"request_id":  r.ID,
		"type":        string(r.Type),
		"table":       r.Table,
		"state":       string(r.State),
		"assigned_to": r.AssignedTo,
	}
}

func escalatedEvent(r domain.Request) map[string]any {
	return map[string]any{
		"request_id": r.ID,
		"type":       string(r.Type),
		"table":      r.Table,
	}
}
