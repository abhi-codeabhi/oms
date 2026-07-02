// Package domain holds the pure notifications model: Message (an outbound
// notification with its delivery lifecycle) and Template (owner/brand-configurable
// copy, per channel) plus template rendering. It imports NO infrastructure (no
// pgx/nats/connect). Rules live here; adapters map this to/from proto and SQL.
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixMessage = "msg"
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// Channel is the delivery medium for a message. Mirrors the proto Channel enum but
// kept as a local type so the domain owns its rules without importing generated code.
type Channel string

const (
	ChannelUnspecified Channel = ""
	ChannelSMS         Channel = "sms"
	ChannelWhatsApp    Channel = "whatsapp"
	ChannelEmail       Channel = "email"
	ChannelPush        Channel = "push"
)

// Valid reports whether c is a known, sendable channel.
func (c Channel) Valid() bool {
	switch c {
	case ChannelSMS, ChannelWhatsApp, ChannelEmail, ChannelPush:
		return true
	default:
		return false
	}
}

// DeliveryStatus is the message lifecycle. QUEUED -> SENT (accepted by provider) ->
// DELIVERED (provider confirmed) with FAILED as the terminal error state.
type DeliveryStatus string

const (
	StatusUnspecified DeliveryStatus = ""
	StatusQueued      DeliveryStatus = "queued"
	StatusSent        DeliveryStatus = "sent"
	StatusDelivered   DeliveryStatus = "delivered"
	StatusFailed      DeliveryStatus = "failed"
)

// Message is an outbound notification and its delivery state. It is multi-tenant:
// every message carries owner_id (tenant root) and optional restaurant_id.
type Message struct {
	ID             string
	OwnerID        string
	RestaurantID   string // "" = owner/brand-level
	Channel        Channel
	To             string
	TemplateID     string
	Vars           map[string]string
	Subject        string // rendered subject (email)
	Body           string // rendered body
	Status         DeliveryStatus
	ProviderID     string // connector id that sent it (e.g. "twilio", "lognotify")
	ProviderRef    string // provider's message reference (for status correlation)
	IdempotencyKey string
	Error          string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// NewMessage constructs and validates a queued message. rendered subject/body come
// from a Template.Render; ownerID is the tenant root (from auth scope, never body).
func NewMessage(ownerID, restaurantID string, channel Channel, to, templateID string, vars map[string]string, subject, body, idemKey string, now time.Time) (Message, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return Message{}, fieldErr("owner_id is required")
	}
	if !channel.Valid() {
		return Message{}, fieldErr("channel is invalid")
	}
	if to = strings.TrimSpace(to); to == "" {
		return Message{}, fieldErr("to (recipient) is required")
	}
	if templateID = strings.TrimSpace(templateID); templateID == "" {
		return Message{}, fieldErr("template is required")
	}
	if vars == nil {
		vars = map[string]string{}
	}
	return Message{
		ID:             ids.New(PrefixMessage),
		OwnerID:        ownerID,
		RestaurantID:   strings.TrimSpace(restaurantID),
		Channel:        channel,
		To:             to,
		TemplateID:     templateID,
		Vars:           vars,
		Subject:        subject,
		Body:           body,
		Status:         StatusQueued, // messages start QUEUED
		IdempotencyKey: strings.TrimSpace(idemKey),
		CreatedAt:      now.UTC(),
		UpdatedAt:      now.UTC(),
	}, nil
}

// MarkSent records provider acceptance: status -> SENT with the provider id + ref.
func (m *Message) MarkSent(providerID, providerRef string, now time.Time) {
	m.Status = StatusSent
	m.ProviderID = providerID
	m.ProviderRef = providerRef
	m.UpdatedAt = now.UTC()
}

// MarkFailed records a send failure: status -> FAILED with the error message.
func (m *Message) MarkFailed(errMsg string, now time.Time) {
	m.Status = StatusFailed
	m.Error = errMsg
	m.UpdatedAt = now.UTC()
}

// ApplyDeliveryStatus advances the message to a provider-reported terminal status
// (DELIVERED or FAILED). It never regresses a message (e.g. a late SENT callback
// after DELIVERED is ignored) so out-of-order webhooks are safe.
func (m *Message) ApplyDeliveryStatus(s DeliveryStatus, now time.Time) bool {
	if !advances(m.Status, s) {
		return false
	}
	m.Status = s
	m.UpdatedAt = now.UTC()
	return true
}

// advances reports whether moving from -> to is a forward transition in the
// lifecycle rank QUEUED(1) < SENT(2) < DELIVERED/FAILED(3, terminal).
func advances(from, to DeliveryStatus) bool {
	return rank(to) > rank(from)
}

func rank(s DeliveryStatus) int {
	switch s {
	case StatusQueued:
		return 1
	case StatusSent:
		return 2
	case StatusDelivered, StatusFailed:
		return 3
	default:
		return 0
	}
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
