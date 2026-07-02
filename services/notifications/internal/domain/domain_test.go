package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/services/notifications/internal/domain"
)

func TestTemplateRender(t *testing.T) {
	tests := []struct {
		name       string
		subject    string
		body       string
		vars       map[string]string
		wantSubj   string
		wantBody   string
	}{
		{"simple", "", "Your code is {{code}}.", map[string]string{"code": "1234"}, "", "Your code is 1234."},
		{"spaces in braces", "", "Hi {{ name }}!", map[string]string{"name": "Sam"}, "", "Hi Sam!"},
		{"missing var -> empty", "", "Link: {{link}}", map[string]string{}, "", "Link: "},
		{"multiple + subject", "Join {{brand}}", "{{inviter}} invited {{name}}", map[string]string{"brand": "BurgerCo", "inviter": "Ada", "name": "Sam"}, "Join BurgerCo", "Ada invited Sam"},
		{"no placeholders", "", "static text", nil, "", "static text"},
		{"unbalanced braces", "", "oops {{code", map[string]string{"code": "9"}, "", "oops {{code"},
		{"repeated var", "", "{{x}}-{{x}}", map[string]string{"x": "z"}, "", "z-z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tmpl := domain.Template{Subject: tc.subject, Body: tc.body}
			gotSubj, gotBody := tmpl.Render(tc.vars)
			if gotSubj != tc.wantSubj {
				t.Fatalf("subject = %q, want %q", gotSubj, tc.wantSubj)
			}
			if gotBody != tc.wantBody {
				t.Fatalf("body = %q, want %q", gotBody, tc.wantBody)
			}
		})
	}
}

func TestNewTemplateValidation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		owner   string
		id      string
		channel domain.Channel
		body    string
		wantErr bool
	}{
		{"ok", "own_1", "otp", domain.ChannelSMS, "hi", false},
		{"no owner", "", "otp", domain.ChannelSMS, "hi", true},
		{"no id", "own_1", "", domain.ChannelSMS, "hi", true},
		{"bad channel", "own_1", "otp", domain.Channel("x"), "hi", true},
		{"no body", "own_1", "otp", domain.ChannelSMS, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewTemplate(tc.owner, tc.id, tc.channel, "", tc.body, now)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}

func TestMessageLifecycle(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	m, err := domain.NewMessage("own_1", "", domain.ChannelSMS, "+91", "otp", nil, "", "code 1", "k1", now)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if m.Status != domain.StatusQueued {
		t.Fatalf("initial status = %q, want queued", m.Status)
	}

	m.MarkSent("twilio", "SM123", now)
	if m.Status != domain.StatusSent || m.ProviderRef != "SM123" {
		t.Fatalf("after sent: %+v", m)
	}

	// forward transitions
	if !m.ApplyDeliveryStatus(domain.StatusDelivered, now) {
		t.Fatal("sent -> delivered should advance")
	}
	if m.ApplyDeliveryStatus(domain.StatusSent, now) {
		t.Fatal("delivered -> sent must not advance (no regression)")
	}
	if m.ApplyDeliveryStatus(domain.StatusDelivered, now) {
		t.Fatal("delivered -> delivered must not advance")
	}
}

func TestNewMessageValidation(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name    string
		owner   string
		channel domain.Channel
		to      string
		tmpl    string
	}{
		{"no owner", "", domain.ChannelSMS, "x", "otp"},
		{"bad channel", "own_1", domain.Channel("x"), "x", "otp"},
		{"no recipient", "own_1", domain.ChannelSMS, "", "otp"},
		{"no template", "own_1", domain.ChannelSMS, "x", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := domain.NewMessage(tc.owner, "", tc.channel, tc.to, tc.tmpl, nil, "", "", "", now)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}
