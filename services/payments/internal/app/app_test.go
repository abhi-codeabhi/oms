package app_test

import (
	"context"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/app"
	"github.com/restorna/platform/services/payments/internal/domain"
)

const rest = "out_test_1"

func fixedNow() time.Time { return time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC) }

func newApp(repo *fakeRepo, hub *fakeHub, fac *fakeFactory) *app.App {
	return app.New(repo, hub, fac, fixedNow)
}

func inr(minor int64) money.Money { return money.New(minor, "INR") }

// --- CreateIntent: resolves provider + persists + idempotent by key ---

func TestCreateIntent(t *testing.T) {
	tests := []struct {
		name        string
		in          app.CreateIntentInput
		provRef     string
		wantErr     bool
		wantStatus  domain.Status
		wantConn    string
	}{
		{
			name: "resolves provider, persists PENDING, returns handoff",
			in: app.CreateIntentInput{
				RestaurantID: rest, BillID: "bill_1", Amount: inr(24000),
				IdempotencyKey: "idem_1", CustomerContact: "+919999",
			},
			provRef: "razorpay_ord_abc", wantStatus: domain.StatusPending, wantConn: "razorpay",
		},
		{
			name:    "missing idempotency key is invalid",
			in:      app.CreateIntentInput{RestaurantID: rest, BillID: "bill_1", Amount: inr(24000)},
			wantErr: true,
		},
		{
			name: "non-positive amount is invalid",
			in: app.CreateIntentInput{
				RestaurantID: rest, BillID: "bill_1", Amount: inr(0), IdempotencyKey: "idem_x",
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			hub := newFakeHub("razorpay")
			fac := newFakeFactory()
			fac.provider.nextRef = tc.provRef
			a := newApp(repo, hub, fac)

			res, err := a.CreateIntent(context.Background(), tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Payment.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", res.Payment.Status, tc.wantStatus)
			}
			if res.Payment.ConnectorID != tc.wantConn {
				t.Errorf("connector = %s, want %s", res.Payment.ConnectorID, tc.wantConn)
			}
			if res.Payment.ProviderRef != tc.provRef {
				t.Errorf("provider_ref = %s, want %s", res.Payment.ProviderRef, tc.provRef)
			}
			// Provider was resolved from the hub, then built.
			if len(hub.resolveCalls) != 1 {
				t.Errorf("hub Resolve called %d times, want 1", len(hub.resolveCalls))
			}
			if len(fac.built) != 1 || fac.built[0] != tc.wantConn {
				t.Errorf("factory built = %v, want [%s]", fac.built, tc.wantConn)
			}
			// Handoff carries the params the customer app needs.
			if res.Handoff["provider_ref"] != tc.provRef || res.Handoff["payment_id"] != res.Payment.ID {
				t.Errorf("handoff missing keys: %v", res.Handoff)
			}
			// Persisted + retrievable.
			got, gerr := a.GetPayment(context.Background(), rest, res.Payment.ID)
			if gerr != nil {
				t.Fatalf("GetPayment: %v", gerr)
			}
			if got.ID != res.Payment.ID {
				t.Errorf("persisted id mismatch")
			}
		})
	}
}

func TestCreateIntent_IdempotentByKey(t *testing.T) {
	repo := newFakeRepo()
	hub := newFakeHub("razorpay")
	fac := newFakeFactory()
	fac.provider.nextRef = "ref_1"
	a := newApp(repo, hub, fac)

	in := app.CreateIntentInput{RestaurantID: rest, BillID: "bill_1", Amount: inr(500), IdempotencyKey: "same"}

	first, err := a.CreateIntent(context.Background(), in)
	if err != nil {
		t.Fatalf("first CreateIntent: %v", err)
	}
	second, err := a.CreateIntent(context.Background(), in)
	if err != nil {
		t.Fatalf("second CreateIntent: %v", err)
	}

	if first.Payment.ID != second.Payment.ID {
		t.Errorf("idempotency broken: %s != %s", first.Payment.ID, second.Payment.ID)
	}
	// The provider must be hit only ONCE (no double charge).
	if fac.provider.createCalls != 1 {
		t.Errorf("provider CreateIntent called %d times, want 1", fac.provider.createCalls)
	}
	// Only one hub resolve (second call short-circuits on the idempotency hit).
	if len(hub.resolveCalls) != 1 {
		t.Errorf("hub Resolve called %d times, want 1", len(hub.resolveCalls))
	}
}

// --- Webhook: captured event flips status + emits payments.captured ---

func TestOnWebhook(t *testing.T) {
	tests := []struct {
		name       string
		captured   bool
		wantStatus domain.Status
		wantEvent  string
	}{
		{"captured flips to CAPTURED + emits captured", true, domain.StatusCaptured, app.EventCaptured},
		{"failed flips to FAILED + emits failed", false, domain.StatusFailed, app.EventFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			hub := newFakeHub("razorpay")
			fac := newFakeFactory()
			fac.provider.nextRef = "prov_ref_9"
			a := newApp(repo, hub, fac)

			created, err := a.CreateIntent(context.Background(), app.CreateIntentInput{
				RestaurantID: rest, BillID: "bill_9", Amount: inr(1000), IdempotencyKey: "k9",
			})
			if err != nil {
				t.Fatalf("CreateIntent: %v", err)
			}

			w := app.WebhookEvent{
				EventID: "evt_1", RestaurantID: rest, ProviderRef: "prov_ref_9",
				Captured: tc.captured, Method: domain.MethodUPI,
			}
			if err := a.OnWebhook(context.Background(), w); err != nil {
				t.Fatalf("OnWebhook: %v", err)
			}

			got, _ := a.GetPayment(context.Background(), rest, created.Payment.ID)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", got.Status, tc.wantStatus)
			}
			if n := countEvents(repo, tc.wantEvent); n != 1 {
				t.Errorf("emitted %d %s events, want 1", n, tc.wantEvent)
			}

			// Idempotent: a redelivered event id is a no-op (no duplicate event).
			if err := a.OnWebhook(context.Background(), w); err != nil {
				t.Fatalf("redelivered OnWebhook: %v", err)
			}
			if n := countEvents(repo, tc.wantEvent); n != 1 {
				t.Errorf("after redelivery emitted %d %s events, want 1", n, tc.wantEvent)
			}
		})
	}
}

func TestOnWebhook_UnknownRefIsDropped(t *testing.T) {
	repo := newFakeRepo()
	a := newApp(repo, newFakeHub("razorpay"), newFakeFactory())
	err := a.OnWebhook(context.Background(), app.WebhookEvent{
		EventID: "evt_x", ProviderRef: "nope", Captured: true,
	})
	if err != nil {
		t.Fatalf("unknown ref should be dropped (ack), got: %v", err)
	}
	if len(repo.events) != 0 {
		t.Errorf("no events should be emitted for an unknown ref, got %d", len(repo.events))
	}
}

// --- Refund path ---

func TestRefund(t *testing.T) {
	repo := newFakeRepo()
	hub := newFakeHub("razorpay")
	fac := newFakeFactory()
	fac.provider.nextRef = "prov_ref_r"
	a := newApp(repo, hub, fac)

	created, err := a.CreateIntent(context.Background(), app.CreateIntentInput{
		RestaurantID: rest, BillID: "bill_r", Amount: inr(2000), IdempotencyKey: "kr",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}
	// Capture it first (webhook path) so it is refundable.
	if err := a.OnWebhook(context.Background(), app.WebhookEvent{
		EventID: "evt_cap", RestaurantID: rest, ProviderRef: "prov_ref_r", Captured: true,
	}); err != nil {
		t.Fatalf("capture webhook: %v", err)
	}

	tests := []struct {
		name       string
		amount     money.Money
		wantStatus domain.Status
		wantErr    bool
	}{
		{"partial refund stays CAPTURED", inr(500), domain.StatusCaptured, false},
		{"remaining refund moves to REFUNDED", inr(1500), domain.StatusRefunded, false},
		{"over-refund is rejected", inr(1), domain.StatusRefunded, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := a.Refund(context.Background(), rest, created.Payment.ID, tc.amount, "customer request")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("Refund: %v", err)
			}
			if p.Status != tc.wantStatus {
				t.Errorf("status = %s, want %s", p.Status, tc.wantStatus)
			}
		})
	}

	// Provider Refund was invoked for each successful refund.
	if len(fac.provider.refundCalls) != 2 {
		t.Errorf("provider Refund called %d times, want 2", len(fac.provider.refundCalls))
	}
	if n := countEvents(repo, app.EventRefunded); n != 2 {
		t.Errorf("emitted %d refunded events, want 2", n)
	}
}

// --- GetPayment ---

func TestGetPayment(t *testing.T) {
	repo := newFakeRepo()
	fac := newFakeFactory()
	fac.provider.nextRef = "ref_g"
	a := newApp(repo, newFakeHub("razorpay"), fac)

	created, err := a.CreateIntent(context.Background(), app.CreateIntentInput{
		RestaurantID: rest, BillID: "bill_g", Amount: inr(750), IdempotencyKey: "kg",
	})
	if err != nil {
		t.Fatalf("CreateIntent: %v", err)
	}

	got, err := a.GetPayment(context.Background(), rest, created.Payment.ID)
	if err != nil {
		t.Fatalf("GetPayment: %v", err)
	}
	if got.ID != created.Payment.ID || got.Amount.Minor != 750 {
		t.Errorf("GetPayment mismatch: %+v", got)
	}

	if _, err := a.GetPayment(context.Background(), rest, "pay_missing"); err == nil {
		t.Errorf("expected NotFound for missing payment")
	}
}
