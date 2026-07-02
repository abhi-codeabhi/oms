// Package events defines the CloudEvents-style envelope every domain event uses.
//
// An Event carries a unique id (used by consumers to dedupe for exactly-once
// effect), a dotted type, the owning tenant, a source, an occurrence time, and
// an opaque JSON data payload.
package events

import (
	"encoding/json"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// Event is the transport envelope for a domain event.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	TenantID   string          `json:"tenant_id"`
	Source     string          `json:"source"`
	OccurredAt time.Time       `json:"occurred_at"`
	Data       json.RawMessage `json:"data"`
}

// New builds an Event of type typ for tenantID, marshalling data to JSON. A
// fresh id and OccurredAt timestamp are assigned. If data fails to marshal the
// Data field is left nil.
func New(typ, tenantID string, data any) Event {
	var raw json.RawMessage
	if data != nil {
		if b, err := json.Marshal(data); err == nil {
			raw = b
		}
	}
	return Event{
		ID:         ids.New("evt"),
		Type:       typ,
		TenantID:   tenantID,
		OccurredAt: time.Now().UTC(),
		Data:       raw,
	}
}
