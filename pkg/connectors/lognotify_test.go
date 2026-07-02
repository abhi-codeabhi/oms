package connectors

import (
	"context"
	"testing"
)

func TestLogNotify_SendReturnsRefAndCounts(t *testing.T) {
	l := NewLogNotify()
	if err := l.Init(context.Background(), nil); err != nil {
		t.Fatalf("init should never fail: %v", err)
	}
	ref, err := l.Send(context.Background(), "sms", "+919812345678", "", "Your code is 1234")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if ref == "" {
		t.Fatal("want a synthetic provider ref")
	}
	if l.Count() != 1 {
		t.Fatalf("count = %d, want 1", l.Count())
	}
}

func TestLogNotify_Manifest(t *testing.T) {
	m := NewLogNotify().Manifest()
	if m.ID != LogNotifyID {
		t.Fatalf("id = %q, want %q", m.ID, LogNotifyID)
	}
	if len(m.Capabilities) != 1 || m.Capabilities[0] != "notification" {
		t.Fatalf("capabilities = %v", m.Capabilities)
	}
}

func TestNewNotification_FactoryAndFallback(t *testing.T) {
	// lognotify is always buildable with no config.
	c, err := NewNotification(LogNotifyID, nil)
	if err != nil {
		t.Fatalf("build lognotify: %v", err)
	}
	if _, err := c.Send(context.Background(), "sms", "+91", "", "hi"); err != nil {
		t.Fatalf("send: %v", err)
	}
	// unknown id errors so the caller can fall back.
	if _, err := NewNotification("nope", nil); err == nil {
		t.Fatal("want error for unknown connector id")
	}
	// registered ids include the built-ins.
	ids := NotificationIDs()
	if len(ids) < 3 {
		t.Fatalf("want >=3 notification ids, got %v", ids)
	}
}

func TestLogNotify_VerifyWebhook(t *testing.T) {
	l := NewLogNotify()
	e, err := l.VerifyWebhook(context.Background(), []byte(`{"provider_ref":"logn_x","status":"delivered"}`), nil)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if e.Type != "restorna.notifications.delivery.updated.v1" {
		t.Fatalf("event type = %q", e.Type)
	}
}
