package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/notifications/internal/app"
	"github.com/restorna/platform/services/notifications/internal/domain"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

const owner = "own_acme"

// seedOTP registers the platform default OTP template (sms) so Send has copy to
// render, mirroring the migration seed.
func seedOTP(r *fakeRepo) {
	r.seedTemplate(domain.Template{
		OwnerID: platformOwner, ID: "otp", Channel: domain.ChannelSMS,
		Body: "Your code is {{code}}.", UpdatedAt: time.Now(),
	})
}

func TestSend_RendersResolvesPersistsAndSends(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	hub := &fakeHub{res: ports.Resolution{ConnectorID: "twilio", Installed: true, Config: map[string]string{"k": "v"}}}
	fac := newFakeFactory()
	a := app.New(repo, hub, fac, fixedClock())

	msg, err := a.Send(context.Background(), app.SendInput{
		OwnerID: owner, Channel: domain.ChannelSMS, To: "+919812345678",
		TemplateID: "otp", Vars: map[string]string{"code": "1234"}, IdempotencyKey: "otp-1",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	if !ids.Valid(domain.PrefixMessage, msg.ID) {
		t.Fatalf("message id %q invalid", msg.ID)
	}
	// rendered body
	if msg.Body != "Your code is 1234." {
		t.Fatalf("body not rendered: %q", msg.Body)
	}
	// resolved provider used + SENT with provider ref
	if msg.Status != domain.StatusSent {
		t.Fatalf("status = %q, want sent", msg.Status)
	}
	if msg.ProviderID != "twilio" || msg.ProviderRef == "" {
		t.Fatalf("provider not recorded: id=%q ref=%q", msg.ProviderID, msg.ProviderRef)
	}
	if len(fac.newCalls) != 1 || fac.newCalls[0] != "twilio" {
		t.Fatalf("factory.New calls = %v, want [twilio]", fac.newCalls)
	}
	// real sender received the rendered body, not the fallback.
	if len(fac.real.sent) != 1 || fac.real.sent[0].Body != "Your code is 1234." {
		t.Fatalf("real sender payload wrong: %+v", fac.real.sent)
	}
	if len(fac.fallback.sent) != 0 {
		t.Fatal("fallback should not have been used")
	}
	// persisted
	if _, ok := repo.messages[msg.ID]; !ok {
		t.Fatal("message not persisted")
	}
	// message.sent event emitted
	if got := countEvents(repo, app.EventMessageSent); got != 1 {
		t.Fatalf("want 1 message.sent event, got %d", got)
	}
}

func TestSend_IdempotentByKey(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	hub := &fakeHub{res: ports.Resolution{ConnectorID: "twilio", Installed: true}}
	fac := newFakeFactory()
	a := app.New(repo, hub, fac, fixedClock())

	in := app.SendInput{OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "otp", Vars: map[string]string{"code": "9"}, IdempotencyKey: "dup"}
	first, err := a.Send(context.Background(), in)
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	second, err := a.Send(context.Background(), in)
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("idempotency broken: %q != %q", first.ID, second.ID)
	}
	// only one dispatch, one message, one event.
	if len(fac.real.sent) != 1 {
		t.Fatalf("want 1 dispatch, got %d", len(fac.real.sent))
	}
	if len(repo.messages) != 1 {
		t.Fatalf("want 1 message persisted, got %d", len(repo.messages))
	}
	if got := countEvents(repo, app.EventMessageSent); got != 1 {
		t.Fatalf("want 1 sent event, got %d", got)
	}
}

func TestSend_FallsBackToMockWhenNoProviderInstalled(t *testing.T) {
	tests := []struct {
		name string
		hub  *fakeHub
		fac  func() *fakeFactory
	}{
		{"none installed", &fakeHub{res: ports.Resolution{Installed: false}}, newFakeFactory},
		{"installed but empty id", &fakeHub{res: ports.Resolution{Installed: true, ConnectorID: ""}}, newFakeFactory},
		{"provider build fails -> mock", &fakeHub{res: ports.Resolution{Installed: true, ConnectorID: "twilio"}}, func() *fakeFactory {
			f := newFakeFactory()
			f.newErr = errors.New("bad config")
			return f
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			seedOTP(repo)
			fac := tc.fac()
			a := app.New(repo, tc.hub, fac, fixedClock())

			msg, err := a.Send(context.Background(), app.SendInput{
				OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "otp",
				Vars: map[string]string{"code": "7"},
			})
			if err != nil {
				t.Fatalf("send should succeed via fallback: %v", err)
			}
			if msg.ProviderID != "lognotify" {
				t.Fatalf("want lognotify provider, got %q", msg.ProviderID)
			}
			if msg.Status != domain.StatusSent {
				t.Fatalf("status = %q, want sent", msg.Status)
			}
			if len(fac.fallback.sent) != 1 {
				t.Fatalf("fallback dispatch count = %d, want 1", len(fac.fallback.sent))
			}
			if len(fac.real.sent) != 0 {
				t.Fatal("real provider should not have been used")
			}
		})
	}
}

func TestSend_ProviderFailureMarksFailed(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	hub := &fakeHub{res: ports.Resolution{Installed: true, ConnectorID: "twilio"}}
	fac := newFakeFactory()
	fac.real.failNext = true
	a := app.New(repo, hub, fac, fixedClock())

	_, err := a.Send(context.Background(), app.SendInput{
		OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "otp", Vars: map[string]string{"code": "1"},
	})
	if err == nil {
		t.Fatal("expected send error")
	}
	// exactly one message persisted, in FAILED state, with a failed event.
	if len(repo.messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(repo.messages))
	}
	for _, m := range repo.messages {
		if m.Status != domain.StatusFailed {
			t.Fatalf("status = %q, want failed", m.Status)
		}
	}
	if got := countEvents(repo, app.EventMessageFailed); got != 1 {
		t.Fatalf("want 1 failed event, got %d", got)
	}
}

func TestSend_MissingTemplate(t *testing.T) {
	repo := newFakeRepo() // no template seeded
	hub := &fakeHub{res: ports.Resolution{Installed: false}}
	a := app.New(repo, hub, newFakeFactory(), fixedClock())

	_, err := a.Send(context.Background(), app.SendInput{
		OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "ghost",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSend_Validation(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	a := app.New(repo, &fakeHub{}, newFakeFactory(), fixedClock())
	tests := []struct {
		name string
		in   app.SendInput
	}{
		{"no owner", app.SendInput{Channel: domain.ChannelSMS, To: "x", TemplateID: "otp"}},
		{"bad channel", app.SendInput{OwnerID: owner, Channel: domain.Channel("carrier-pigeon"), To: "x", TemplateID: "otp"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.Send(context.Background(), tc.in); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestUpsertAndListTemplates(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, &fakeHub{}, newFakeFactory(), fixedClock())

	// upsert two, then update one.
	if _, err := a.UpsertTemplate(context.Background(), app.UpsertTemplateInput{OwnerID: owner, ID: "otp", Channel: domain.ChannelSMS, Body: "code {{code}}"}); err != nil {
		t.Fatalf("upsert otp: %v", err)
	}
	if _, err := a.UpsertTemplate(context.Background(), app.UpsertTemplateInput{OwnerID: owner, ID: "invite", Channel: domain.ChannelEmail, Subject: "Join {{brand}}", Body: "link {{link}}"}); err != nil {
		t.Fatalf("upsert invite: %v", err)
	}
	// replace otp body.
	if _, err := a.UpsertTemplate(context.Background(), app.UpsertTemplateInput{OwnerID: owner, ID: "otp", Channel: domain.ChannelSMS, Body: "your code {{code}}"}); err != nil {
		t.Fatalf("replace otp: %v", err)
	}

	list, err := a.ListTemplates(context.Background(), owner)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("want 2 templates, got %d", len(list))
	}
	// sorted by id: invite, otp
	if list[0].ID != "invite" || list[1].ID != "otp" {
		t.Fatalf("unexpected order: %q, %q", list[0].ID, list[1].ID)
	}
	if list[1].Body != "your code {{code}}" {
		t.Fatalf("otp body not replaced: %q", list[1].Body)
	}
}

func TestUpsertTemplate_Validation(t *testing.T) {
	a := app.New(newFakeRepo(), &fakeHub{}, newFakeFactory(), fixedClock())
	tests := []struct {
		name string
		in   app.UpsertTemplateInput
	}{
		{"no id", app.UpsertTemplateInput{OwnerID: owner, Channel: domain.ChannelSMS, Body: "x"}},
		{"no body", app.UpsertTemplateInput{OwnerID: owner, ID: "otp", Channel: domain.ChannelSMS}},
		{"bad channel", app.UpsertTemplateInput{OwnerID: owner, ID: "otp", Channel: domain.Channel("x"), Body: "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := a.UpsertTemplate(context.Background(), tc.in); !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestGetStatus(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	hub := &fakeHub{res: ports.Resolution{Installed: true, ConnectorID: "twilio"}}
	a := app.New(repo, hub, newFakeFactory(), fixedClock())

	sent, err := a.Send(context.Background(), app.SendInput{OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "otp", Vars: map[string]string{"code": "1"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	got, err := a.GetStatus(context.Background(), owner, sent.ID)
	if err != nil {
		t.Fatalf("get status: %v", err)
	}
	if got.ID != sent.ID || got.Status != domain.StatusSent {
		t.Fatalf("unexpected status: %+v", got)
	}

	if _, err := a.GetStatus(context.Background(), owner, ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty id err = %v, want ErrInvalid", err)
	}
	if _, err := a.GetStatus(context.Background(), owner, ids.New(domain.PrefixMessage)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown id err = %v, want ErrNotFound", err)
	}
}

func TestApplyDeliveryStatus_AdvancesAndDedupes(t *testing.T) {
	repo := newFakeRepo()
	seedOTP(repo)
	hub := &fakeHub{res: ports.Resolution{Installed: true, ConnectorID: "twilio"}}
	fac := newFakeFactory()
	a := app.New(repo, hub, fac, fixedClock())

	sent, err := a.Send(context.Background(), app.SendInput{OwnerID: owner, Channel: domain.ChannelSMS, To: "+91981", TemplateID: "otp", Vars: map[string]string{"code": "1"}})
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// deliver the message via a webhook event.
	if err := a.ApplyDeliveryStatus(context.Background(), "evt_1", sent.ProviderID, sent.ProviderRef, domain.StatusDelivered); err != nil {
		t.Fatalf("apply delivered: %v", err)
	}
	got, _ := a.GetStatus(context.Background(), owner, sent.ID)
	if got.Status != domain.StatusDelivered {
		t.Fatalf("status = %q, want delivered", got.Status)
	}
	if got := countEvents(repo, app.EventMessageUpdated); got != 1 {
		t.Fatalf("want 1 updated event, got %d", got)
	}

	// duplicate event id is a no-op (exactly-once).
	if err := a.ApplyDeliveryStatus(context.Background(), "evt_1", sent.ProviderID, sent.ProviderRef, domain.StatusFailed); err != nil {
		t.Fatalf("apply dup: %v", err)
	}
	got, _ = a.GetStatus(context.Background(), owner, sent.ID)
	if got.Status != domain.StatusDelivered {
		t.Fatalf("dup event must not change status, got %q", got.Status)
	}

	// a fresh event that would regress (delivered -> sent) is ignored.
	if err := a.ApplyDeliveryStatus(context.Background(), "evt_2", sent.ProviderID, sent.ProviderRef, domain.StatusSent); err != nil {
		t.Fatalf("apply regress: %v", err)
	}
	got, _ = a.GetStatus(context.Background(), owner, sent.ID)
	if got.Status != domain.StatusDelivered {
		t.Fatalf("regression must be ignored, got %q", got.Status)
	}

	// unknown provider ref is a no-op (not an error).
	if err := a.ApplyDeliveryStatus(context.Background(), "evt_3", "twilio", "nope", domain.StatusDelivered); err != nil {
		t.Fatalf("unknown ref should be nil err, got %v", err)
	}
}
