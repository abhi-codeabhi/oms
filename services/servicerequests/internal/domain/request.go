// Package domain holds the pure service-requests model: the Request aggregate
// (a guest-raised "call waiter / water / bill / cutlery" at a table) plus the
// rate-limit (cooldown) and escalation rules. It imports NO infrastructure
// (no pgx, nats, connect) — only pkg/ids for ULID generation. Time is always
// passed in explicitly as `now` so escalation/cooldown logic is fully
// deterministic in tests. Ported from the proven Restorna Node service-requests
// (raise with per table+type cooldown, escalateDue past a threshold, acknowledge
// records the cooldown, listOpen = state != done).
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// PrefixRequest is the type-prefixed ULID namespace for a request id (req_...).
const PrefixRequest = "req"

// Type is the kind of service a guest can request from the table.
type Type string

const (
	TypeCall    Type = "call"
	TypeWater   Type = "water"
	TypeBill    Type = "bill"
	TypeCutlery Type = "cutlery"
)

// validTypes is the closed set a guest may raise; anything else is ErrInvalid.
var validTypes = map[Type]struct{}{
	TypeCall:    {},
	TypeWater:   {},
	TypeBill:    {},
	TypeCutlery: {},
}

// State is the request lifecycle: assigned -> escalated -> done.
type State string

const (
	// StateAssigned: a waiter owns the request; it is eligible for escalation.
	StateAssigned State = "assigned"
	// StateEscalated: nobody owns it (raised unassigned) or it sat too long —
	// surface it on the escalation queue.
	StateEscalated State = "escalated"
	// StateDone: acknowledged / completed (sets the cooldown for table+type).
	StateDone State = "done"
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid   = errors.New("invalid argument")
	ErrNotFound  = errors.New("not found")
	ErrCooldown  = errors.New("request is in cooldown")
)

// Request is a guest-raised waiter call tied to a table.
type Request struct {
	ID         string
	Type       Type
	Table      int32
	State      State
	AssignedTo string
	CreatedAt  time.Time
	AckedAt    time.Time // zero until acknowledged
}

// ValidateRaise checks the inbound raise fields, returning a typed ErrInvalid on
// any problem. table must be positive and type must be one of the known kinds.
func ValidateRaise(typ Type, table int32) error {
	if table <= 0 {
		return fieldErr("table must be a positive number")
	}
	if _, ok := validTypes[typ]; !ok {
		return fieldErr("type must be one of call, water, bill, cutlery")
	}
	return nil
}

// Raise builds a fresh request. If a waiter is already assigned it starts
// 'assigned' (eligible for escalation); otherwise it is 'escalated' immediately
// (nobody owns it yet — surface it on the escalation queue). Mirrors the Node
// demo's raise(). Caller validates first (or relies on ValidateRaise here).
func Raise(typ Type, table int32, assignedTo string, now time.Time) (Request, error) {
	if err := ValidateRaise(typ, table); err != nil {
		return Request{}, err
	}
	assignedTo = strings.TrimSpace(assignedTo)
	state := StateEscalated
	if assignedTo != "" {
		state = StateAssigned
	}
	return Request{
		ID:         ids.New(PrefixRequest),
		Type:       typ,
		Table:      table,
		State:      state,
		AssignedTo: assignedTo,
		CreatedAt:  now.UTC(),
	}, nil
}

// CanRaise reports whether a guest may raise this table+type now. Either nothing
// was ever acknowledged for the table+type (lastAck is zero), or the cooldown
// window has fully elapsed. Pure mirror of the Node demo's canRaise().
func CanRaise(lastAck time.Time, now time.Time, cooldown time.Duration) bool {
	if lastAck.IsZero() {
		return true
	}
	return now.Sub(lastAck) >= cooldown
}

// ShouldEscalate reports whether an assigned request has waited >= the escalation
// threshold without being acknowledged. Only 'assigned' requests escalate;
// already-escalated or done requests never re-escalate.
func ShouldEscalate(r Request, now time.Time, threshold time.Duration) bool {
	return r.State == StateAssigned && now.Sub(r.CreatedAt) >= threshold
}

// Escalate flips a request to 'escalated' in place.
func (r *Request) Escalate() { r.State = StateEscalated }

// Acknowledge marks the request done and stamps AckedAt with now (this is the
// moment the table+type cooldown starts).
func (r *Request) Acknowledge(now time.Time) {
	r.State = StateDone
	r.AckedAt = now.UTC()
}

// IsOpen reports whether the request is still outstanding (not done). ListOpen
// returns exactly these (assigned + escalated).
func (r Request) IsOpen() bool { return r.State != StateDone }

// OpenOnly filters a slice to the open requests (state != done).
func OpenOnly(in []Request) []Request {
	out := make([]Request, 0, len(in))
	for _, r := range in {
		if r.IsOpen() {
			out = append(out, r)
		}
	}
	return out
}

// --- helpers ---

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
