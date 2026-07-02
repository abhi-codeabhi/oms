// Package domain holds the pure Payment model and its status machine. It imports
// NO infrastructure (no pgx, nats, connect, or the connector SDK). Rules live
// here; adapters map this to/from proto, SQL, and provider calls.
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// PrefixPayment is the type-prefix for payment ids (see CONVENTIONS.md).
const PrefixPayment = "pay"

// Status is the payment lifecycle state. Mirrors payments.v1.Status.
type Status string

const (
	StatusCreated  Status = "CREATED"  // intent minted, awaiting customer action
	StatusPending  Status = "PENDING"  // provider handoff done, awaiting confirmation
	StatusCaptured Status = "CAPTURED" // funds captured (terminal-success)
	StatusFailed   Status = "FAILED"   // provider reported failure (terminal)
	StatusRefunded Status = "REFUNDED" // fully/partially refunded (terminal)
)

// Payment methods (free-form provider hint).
const (
	MethodUPI        = "upi"
	MethodCard       = "card"
	MethodWallet     = "wallet"
	MethodNetbanking = "netbanking"
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid          = errors.New("invalid argument")
	ErrNotFound         = errors.New("not found")
	ErrInvalidState     = errors.New("invalid state transition")
	ErrAmountMismatch   = errors.New("amount mismatch")
	ErrRefundExceeds    = errors.New("refund exceeds captured amount")
	ErrCurrencyMismatch = errors.New("currency mismatch")
)

// Payment is the aggregate: one online-money attempt for a bill, orchestrated
// over a resolved connector. Money is always integer minor units + currency.
type Payment struct {
	ID           string
	RestaurantID string // tenant key (RLS)
	BillID       string
	Amount       money.Money
	ConnectorID  string // resolved provider id (razorpay|paytm|phonepe|mock)
	ProviderRef  string // gateway order/txn id (match key for webhooks)
	Status       Status
	Method       string
	Refunded     money.Money // cumulative refunded amount (minor units)
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// NewPayment constructs a validated Payment in CREATED state. restaurantID is the
// trusted tenant scope (from auth/event envelope), never from the request body.
func NewPayment(restaurantID, billID string, amount money.Money, connectorID string, now time.Time) (Payment, error) {
	restaurantID = strings.TrimSpace(restaurantID)
	if restaurantID == "" {
		return Payment{}, fieldErr("restaurant scope is required")
	}
	if billID = strings.TrimSpace(billID); billID == "" {
		return Payment{}, fieldErr("bill_id is required")
	}
	if amount.Minor <= 0 {
		return Payment{}, fieldErr("amount must be a positive minor-unit value")
	}
	if amount.Currency == "" {
		return Payment{}, fieldErr("amount currency is required")
	}
	if connectorID = strings.TrimSpace(connectorID); connectorID == "" {
		return Payment{}, fieldErr("connector_id is required")
	}
	return Payment{
		ID:           ids.New(PrefixPayment),
		RestaurantID: restaurantID,
		BillID:       billID,
		Amount:       amount,
		ConnectorID:  connectorID,
		Status:       StatusCreated,
		Refunded:     money.New(0, amount.Currency),
		CreatedAt:    now.UTC(),
		UpdatedAt:    now.UTC(),
	}, nil
}

// AttachProvider records the gateway ref returned by CreateIntent and advances the
// payment to PENDING (handoff issued, awaiting the customer + webhook).
func (p *Payment) AttachProvider(providerRef string, now time.Time) error {
	if p.Status != StatusCreated {
		return ErrInvalidState
	}
	if strings.TrimSpace(providerRef) == "" {
		return fieldErr("provider_ref is required")
	}
	p.ProviderRef = providerRef
	p.Status = StatusPending
	p.UpdatedAt = now.UTC()
	return nil
}

// MarkCaptured moves a CREATED/PENDING payment to CAPTURED. Idempotent: calling it
// on an already-captured payment is a no-op (webhooks may redeliver).
func (p *Payment) MarkCaptured(method string, now time.Time) error {
	if p.Status == StatusCaptured {
		return nil
	}
	if p.Status != StatusCreated && p.Status != StatusPending {
		return ErrInvalidState
	}
	if method != "" {
		p.Method = method
	}
	p.Status = StatusCaptured
	p.UpdatedAt = now.UTC()
	return nil
}

// MarkFailed moves a CREATED/PENDING payment to FAILED. Idempotent on FAILED; a
// captured payment cannot fail (guard against out-of-order webhooks).
func (p *Payment) MarkFailed(now time.Time) error {
	if p.Status == StatusFailed {
		return nil
	}
	if p.Status == StatusCaptured || p.Status == StatusRefunded {
		return ErrInvalidState
	}
	p.Status = StatusFailed
	p.UpdatedAt = now.UTC()
	return nil
}

// ApplyRefund records a refund against a CAPTURED (or already partially REFUNDED)
// payment. amount must share the payment currency and not exceed the remaining
// captured balance. Fully-refunded payments move to REFUNDED.
func (p *Payment) ApplyRefund(amount money.Money, now time.Time) error {
	if p.Status != StatusCaptured && p.Status != StatusRefunded {
		return ErrInvalidState
	}
	if amount.Currency != p.Amount.Currency {
		return ErrCurrencyMismatch
	}
	if amount.Minor <= 0 {
		return fieldErr("refund amount must be positive")
	}
	newTotal := p.Refunded.Minor + amount.Minor
	if newTotal > p.Amount.Minor {
		return ErrRefundExceeds
	}
	p.Refunded = money.New(newTotal, p.Amount.Currency)
	if newTotal == p.Amount.Minor {
		p.Status = StatusRefunded
	}
	p.UpdatedAt = now.UTC()
	return nil
}

// CanCapture reports whether an explicit Capture RPC is valid (auth+capture flow).
func (p *Payment) CanCapture() bool {
	return p.Status == StatusCreated || p.Status == StatusPending
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
