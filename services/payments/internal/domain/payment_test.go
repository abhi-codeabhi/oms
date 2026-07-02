package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/domain"
)

func now() time.Time { return time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC) }

func newP(t *testing.T) domain.Payment {
	t.Helper()
	p, err := domain.NewPayment("out_1", "bill_1", money.New(1000, "INR"), "razorpay", now())
	if err != nil {
		t.Fatalf("NewPayment: %v", err)
	}
	return p
}

func TestNewPaymentValidation(t *testing.T) {
	tests := []struct {
		name         string
		restaurantID string
		billID       string
		amount       money.Money
		connectorID  string
		wantErr      bool
	}{
		{"valid", "out_1", "bill_1", money.New(100, "INR"), "razorpay", false},
		{"empty restaurant", "", "bill_1", money.New(100, "INR"), "razorpay", true},
		{"empty bill", "out_1", "", money.New(100, "INR"), "razorpay", true},
		{"zero amount", "out_1", "bill_1", money.New(0, "INR"), "razorpay", true},
		{"empty currency", "out_1", "bill_1", money.New(100, ""), "razorpay", true},
		{"empty connector", "out_1", "bill_1", money.New(100, "INR"), "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := domain.NewPayment(tc.restaurantID, tc.billID, tc.amount, tc.connectorID, now())
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				if !errors.Is(err, domain.ErrInvalid) {
					t.Errorf("expected ErrInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if p.Status != domain.StatusCreated {
				t.Errorf("status = %s, want CREATED", p.Status)
			}
		})
	}
}

func TestStatusMachine(t *testing.T) {
	// AttachProvider: CREATED -> PENDING.
	p := newP(t)
	if err := p.AttachProvider("ref", now()); err != nil {
		t.Fatalf("AttachProvider: %v", err)
	}
	if p.Status != domain.StatusPending {
		t.Fatalf("want PENDING, got %s", p.Status)
	}
	// Second AttachProvider is invalid.
	if err := p.AttachProvider("ref2", now()); !errors.Is(err, domain.ErrInvalidState) {
		t.Errorf("want ErrInvalidState, got %v", err)
	}

	// MarkCaptured: PENDING -> CAPTURED, idempotent.
	if err := p.MarkCaptured("upi", now()); err != nil {
		t.Fatalf("MarkCaptured: %v", err)
	}
	if p.Status != domain.StatusCaptured || p.Method != "upi" {
		t.Fatalf("want CAPTURED/upi, got %s/%s", p.Status, p.Method)
	}
	if err := p.MarkCaptured("upi", now()); err != nil {
		t.Errorf("MarkCaptured should be idempotent, got %v", err)
	}

	// A captured payment cannot fail.
	if err := p.MarkFailed(now()); !errors.Is(err, domain.ErrInvalidState) {
		t.Errorf("captured->failed should be invalid, got %v", err)
	}
}

func TestRefundRules(t *testing.T) {
	p := newP(t)
	_ = p.AttachProvider("ref", now())
	_ = p.MarkCaptured("card", now())

	// Currency mismatch rejected.
	if err := p.ApplyRefund(money.New(100, "USD"), now()); !errors.Is(err, domain.ErrCurrencyMismatch) {
		t.Errorf("want ErrCurrencyMismatch, got %v", err)
	}
	// Partial refund keeps CAPTURED.
	if err := p.ApplyRefund(money.New(400, "INR"), now()); err != nil {
		t.Fatalf("partial refund: %v", err)
	}
	if p.Status != domain.StatusCaptured || p.Refunded.Minor != 400 {
		t.Errorf("want CAPTURED/400, got %s/%d", p.Status, p.Refunded.Minor)
	}
	// Over-refund rejected.
	if err := p.ApplyRefund(money.New(700, "INR"), now()); !errors.Is(err, domain.ErrRefundExceeds) {
		t.Errorf("want ErrRefundExceeds, got %v", err)
	}
	// Remaining refund moves to REFUNDED.
	if err := p.ApplyRefund(money.New(600, "INR"), now()); err != nil {
		t.Fatalf("final refund: %v", err)
	}
	if p.Status != domain.StatusRefunded || p.Refunded.Minor != 1000 {
		t.Errorf("want REFUNDED/1000, got %s/%d", p.Status, p.Refunded.Minor)
	}
}

func TestMarkFailed(t *testing.T) {
	p := newP(t)
	if err := p.MarkFailed(now()); err != nil {
		t.Fatalf("MarkFailed from CREATED: %v", err)
	}
	if p.Status != domain.StatusFailed {
		t.Errorf("want FAILED, got %s", p.Status)
	}
	// Idempotent.
	if err := p.MarkFailed(now()); err != nil {
		t.Errorf("MarkFailed should be idempotent, got %v", err)
	}
}
