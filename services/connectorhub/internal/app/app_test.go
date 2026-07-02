package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/restorna/platform/services/connectorhub/internal/app"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
)

const owner = "own_test"

// harness wires the app with fakes and returns the pieces tests assert on.
type harness struct {
	uc    *app.App
	repo  *fakeRepo
	ents  *fakeEntitlements
	conns *fakeConnectors
	bus   *fakeBus
}

func newHarness(conns *fakeConnectors) *harness {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	bus := &fakeBus{}
	uc := app.New(repo, ents, newFakeCrypto(), conns, bus, nil)
	return &harness{uc: uc, repo: repo, ents: ents, conns: conns, bus: bus}
}

// --- ListAvailable filters by entitlement ---

func TestListAvailable_FiltersByEntitlement(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
		fakeConn{manifest: manifestWith("zomato", nil, domain.CapabilityAggregator)},
		fakeConn{manifest: manifestWith("hubspot", nil, domain.CapabilityCRM)},
	)

	tests := []struct {
		name     string
		features map[string]bool
		want     map[string]bool // connector ids expected in the marketplace
	}{
		{
			name:     "no features -> nothing visible",
			features: map[string]bool{},
			want:     map[string]bool{},
		},
		{
			name:     "payments only -> just razorpay",
			features: map[string]bool{"payments": true},
			want:     map[string]bool{"razorpay": true},
		},
		{
			name:     "payments + aggregators -> razorpay + zomato",
			features: map[string]bool{"payments": true, "aggregators": true},
			want:     map[string]bool{"razorpay": true, "zomato": true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(conns)
			h.ents.features = tc.features
			got, err := h.uc.ListAvailable(context.Background(), owner)
			if err != nil {
				t.Fatalf("ListAvailable: %v", err)
			}
			gotIDs := map[string]bool{}
			for _, m := range got {
				gotIDs[m.ID] = true
			}
			if len(gotIDs) != len(tc.want) {
				t.Fatalf("visible connectors = %v, want %v", gotIDs, tc.want)
			}
			for id := range tc.want {
				if !gotIDs[id] {
					t.Errorf("expected %q visible, got %v", id, gotIDs)
				}
			}
		})
	}
}

// --- Install reserves quota + encrypts secrets ---

func TestInstall_ReservesQuotaAndEncryptsSecrets(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}

	inst, err := h.uc.Install(context.Background(), app.InstallInput{
		OwnerID:     owner,
		ConnectorID: "razorpay",
		Config: map[string]string{
			"key_id":     "rzp_live_abc", // public
			"key_secret": "supersecret",  // secret
		},
	})
	if err != nil {
		t.Fatalf("Install: %v", err)
	}

	// Quota reserved exactly once for "connectors".
	if len(h.ents.reserveCalls) != 1 || h.ents.reserveCalls[0].Key != domain.QuotaConnectors {
		t.Fatalf("expected 1 connectors reservation, got %+v", h.ents.reserveCalls)
	}
	if h.ents.reserveCalls[0].Delta != 1 {
		t.Errorf("reserve delta = %d, want 1", h.ents.reserveCalls[0].Delta)
	}

	// Secret encrypted at rest: ciphertext present, plaintext NOT recoverable by eye.
	if len(inst.SecretConfig) == 0 {
		t.Fatal("expected non-empty secret ciphertext")
	}
	if containsPlain(inst.SecretConfig, "supersecret") {
		t.Fatal("plaintext secret leaked into stored ciphertext")
	}
	// Public config stored plaintext; secret NOT in public.
	if inst.PublicConfig["key_id"] != "rzp_live_abc" {
		t.Errorf("public key_id = %q", inst.PublicConfig["key_id"])
	}
	if _, ok := inst.PublicConfig["key_secret"]; ok {
		t.Error("secret key must not be in public config")
	}

	// Install lifecycle event staged.
	if len(h.repo.events) != 1 || h.repo.events[0].Type != app.EventConnectorInstalled {
		t.Errorf("expected install event, got %+v", h.repo.events)
	}
}

func TestInstall_OverQuotaBlocked(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
		fakeConn{manifest: manifestWith("paytm", []string{"merchant_key"}, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}
	h.ents.limits = map[string]int64{domain.QuotaConnectors: 1}

	if _, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "razorpay"}); err != nil {
		t.Fatalf("first install: %v", err)
	}
	_, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "paytm"})
	if !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("second install err = %v, want ErrQuotaExceeded", err)
	}
}

func TestInstall_ForbiddenWhenPlanLocksCapability(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("zomato", nil, domain.CapabilityAggregator)},
	)
	h := newHarness(conns)
	// aggregators feature OFF.
	_, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "zomato"})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("err = %v, want ErrForbidden", err)
	}
	if len(h.ents.reserveCalls) != 0 {
		t.Error("must not reserve quota when capability is forbidden")
	}
}

func TestInstall_CompensatesQuotaOnPersistFailure(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}
	h.repo.failInsert = true

	_, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "razorpay"})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if len(h.ents.releaseCalls) != 1 {
		t.Fatalf("expected 1 quota release (compensation), got %d", len(h.ents.releaseCalls))
	}
}

// --- Resolve decrypts ---

func TestResolve_DecryptsConfig(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}

	if _, err := h.uc.Install(context.Background(), app.InstallInput{
		OwnerID:     owner,
		ConnectorID: "razorpay",
		Config:      map[string]string{"key_id": "rzp_live_abc", "key_secret": "supersecret"},
	}); err != nil {
		t.Fatalf("Install: %v", err)
	}

	got, err := h.uc.Resolve(context.Background(), owner, domain.CapabilityPayment, "")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ConnectorID != "razorpay" {
		t.Errorf("connector = %q, want razorpay", got.ConnectorID)
	}
	// Decrypted config carries BOTH public and secret values.
	if got.Config["key_id"] != "rzp_live_abc" {
		t.Errorf("key_id = %q", got.Config["key_id"])
	}
	if got.Config["key_secret"] != "supersecret" {
		t.Errorf("decrypted key_secret = %q, want supersecret", got.Config["key_secret"])
	}
}

func TestResolve_PrefersRequestedConnectorAndSkipsDisabled(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", nil, domain.CapabilityPayment)},
		fakeConn{manifest: manifestWith("paytm", nil, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}
	h.ents.limits = map[string]int64{domain.QuotaConnectors: -1}

	if _, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "razorpay"}); err != nil {
		t.Fatal(err)
	}
	pay, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: "paytm"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := h.uc.Resolve(context.Background(), owner, domain.CapabilityPayment, "paytm")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.ConnectorID != "paytm" {
		t.Errorf("preferred connector = %q, want paytm", got.ConnectorID)
	}

	// Disable paytm -> resolve should fall back to razorpay.
	if _, err := h.uc.UpdateInstallation(context.Background(), app.UpdateInput{
		OwnerID: owner, InstallationID: pay.ID, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	got, err = h.uc.Resolve(context.Background(), owner, domain.CapabilityPayment, "paytm")
	if err != nil {
		t.Fatalf("Resolve after disable: %v", err)
	}
	if got.ConnectorID != "razorpay" {
		t.Errorf("after disabling paytm, resolved = %q, want razorpay", got.ConnectorID)
	}
}

func TestResolve_NotFoundWhenNoConnector(t *testing.T) {
	h := newHarness(newFakeConnectors())
	_, err := h.uc.Resolve(context.Background(), owner, domain.CapabilityPayment, "")
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// --- ListInstallations hides secrets ---

func TestListInstallations_HidesSecrets(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", []string{"key_secret"}, domain.CapabilityPayment)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true}
	if _, err := h.uc.Install(context.Background(), app.InstallInput{
		OwnerID:     owner,
		ConnectorID: "razorpay",
		Config:      map[string]string{"key_id": "rzp_live_abc", "key_secret": "supersecret"},
	}); err != nil {
		t.Fatal(err)
	}

	list, err := h.uc.ListInstallations(context.Background(), owner, "")
	if err != nil {
		t.Fatalf("ListInstallations: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d, want 1", len(list))
	}
	// The public config never carries the secret; the grpc layer echoes only public.
	if _, ok := list[0].PublicConfig["key_secret"]; ok {
		t.Error("secret leaked into public config returned by ListInstallations")
	}
	if list[0].PublicConfig["key_id"] != "rzp_live_abc" {
		t.Errorf("public key_id = %q", list[0].PublicConfig["key_id"])
	}
}

func TestListInstallations_CapabilityFilter(t *testing.T) {
	conns := newFakeConnectors(
		fakeConn{manifest: manifestWith("razorpay", nil, domain.CapabilityPayment)},
		fakeConn{manifest: manifestWith("zomato", nil, domain.CapabilityAggregator)},
	)
	h := newHarness(conns)
	h.ents.features = map[string]bool{"payments": true, "aggregators": true}
	h.ents.limits = map[string]int64{domain.QuotaConnectors: -1}
	for _, id := range []string{"razorpay", "zomato"} {
		if _, err := h.uc.Install(context.Background(), app.InstallInput{OwnerID: owner, ConnectorID: id}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := h.uc.ListInstallations(context.Background(), owner, domain.CapabilityAggregator)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ConnectorID != "zomato" {
		t.Fatalf("aggregator filter = %+v, want just zomato", got)
	}
}

// --- IngestWebhook: valid accepted, tampered rejected, publishes ---

func TestIngestWebhook(t *testing.T) {
	const goodSig = "valid-signature"
	conns := newFakeConnectors(
		fakeConn{
			manifest:    manifestWith("razorpay", []string{"webhook_secret"}, domain.CapabilityPayment),
			eventType:   "restorna.payments.captured.v1",
			expectedSig: goodSig,
		},
	)

	tests := []struct {
		name        string
		sig         string
		wantAccept  bool
		wantErr     bool
		wantPublish int
	}{
		{name: "valid signature accepted + published", sig: goodSig, wantAccept: true, wantPublish: 1},
		{name: "tampered signature rejected, nothing published", sig: "forged", wantErr: true, wantPublish: 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(conns)
			res, err := h.uc.IngestWebhook(context.Background(), app.WebhookInput{
				ConnectorID: "razorpay",
				Body:        []byte(`{"event":"payment.captured"}`),
				Headers:     map[string]string{"X-Signature": tc.sig},
			})
			if tc.wantErr {
				if !errors.Is(err, domain.ErrWebhookUnverified) {
					t.Fatalf("err = %v, want ErrWebhookUnverified", err)
				}
			} else {
				if err != nil {
					t.Fatalf("IngestWebhook: %v", err)
				}
				if !res.Accepted {
					t.Error("expected accepted=true")
				}
				if res.EventType != "restorna.payments.captured.v1" {
					t.Errorf("event_type = %q", res.EventType)
				}
			}
			if got := len(h.bus.published); got != tc.wantPublish {
				t.Errorf("published = %d, want %d", got, tc.wantPublish)
			}
		})
	}
}

// containsPlain reports whether needle appears verbatim in b (used to prove the
// stored secret is ciphertext, not plaintext).
func containsPlain(b []byte, needle string) bool {
	n := []byte(needle)
	if len(n) == 0 || len(b) < len(n) {
		return false
	}
	for i := 0; i+len(n) <= len(b); i++ {
		if string(b[i:i+len(n)]) == needle {
			return true
		}
	}
	return false
}
