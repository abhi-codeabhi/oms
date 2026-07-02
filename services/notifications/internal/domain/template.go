package domain

import (
	"strings"
	"time"
)

// Template is owner/brand-configurable copy for a channel. Its id is a stable,
// human-authored key (e.g. "otp", "staff_invite", "receipt") that callers reference
// in Send. Body/subject carry {{var}} placeholders rendered with the Send vars.
//
// Templates are tenant-scoped: an owner may override the platform's default copy per
// channel. The id + channel + owner form the logical key.
type Template struct {
	OwnerID   string
	ID        string
	Channel   Channel
	Subject   string // used for email; ignored for SMS/WhatsApp/push
	Body      string
	UpdatedAt time.Time
}

// NewTemplate constructs and validates a template. id and body are required;
// channel must be a known channel.
func NewTemplate(ownerID, id string, channel Channel, subject, body string, now time.Time) (Template, error) {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return Template{}, fieldErr("owner_id is required")
	}
	if id = strings.TrimSpace(id); id == "" {
		return Template{}, fieldErr("template id is required")
	}
	if !channel.Valid() {
		return Template{}, fieldErr("channel is invalid")
	}
	if strings.TrimSpace(body) == "" {
		return Template{}, fieldErr("body is required")
	}
	return Template{
		OwnerID:   ownerID,
		ID:        id,
		Channel:   channel,
		Subject:   subject,
		Body:      body,
		UpdatedAt: now.UTC(),
	}, nil
}

// Render substitutes {{key}} placeholders in subject and body with the values in
// vars. Unknown placeholders are left as the empty string (so a missing var never
// leaks the raw {{token}} to a customer). Whitespace inside the braces is tolerated:
// {{ name }} and {{name}} both resolve to vars["name"].
func (t Template) Render(vars map[string]string) (subject, body string) {
	return renderTemplate(t.Subject, vars), renderTemplate(t.Body, vars)
}

// renderTemplate is a tiny, dependency-free {{var}} substitution. It scans for
// "{{" ... "}}" spans and replaces each with vars[trimmed-key] (empty if absent).
func renderTemplate(s string, vars map[string]string) string {
	if s == "" || !strings.Contains(s, "{{") {
		return s
	}
	var b strings.Builder
	for {
		open := strings.Index(s, "{{")
		if open < 0 {
			b.WriteString(s)
			break
		}
		close := strings.Index(s[open:], "}}")
		if close < 0 {
			// Unbalanced braces: emit the rest verbatim.
			b.WriteString(s)
			break
		}
		close += open
		b.WriteString(s[:open])
		key := strings.TrimSpace(s[open+2 : close])
		if v, ok := vars[key]; ok {
			b.WriteString(v)
		}
		s = s[close+2:]
	}
	return b.String()
}
