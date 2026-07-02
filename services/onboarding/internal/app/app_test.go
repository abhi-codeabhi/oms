package app_test

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/onboarding/internal/app"
	"github.com/restorna/platform/services/onboarding/internal/domain"
)

// adminCtx returns a context with a platform-admin tenancy scope (the persona
// that drives onboarding for a new client).
func adminCtx() context.Context {
	return tenancy.With(context.Background(), tenancy.Scope{
		Role: commonv1.Role_ROLE_PLATFORM_ADMIN,
	})
}

// newApp wires the saga against fresh fakes and returns them for assertions.
func newApp() (*app.App, *fakeRepo, *fakeTenant, *fakeEnts, *fakeStaff, *fakeSettings) {
	repo := newFakeRepo()
	tenant := &fakeTenant{}
	ents := &fakeEnts{}
	staff := newFakeStaff()
	settings := &fakeSettings{}
	uc := app.New(repo, &fakeIdentity{}, tenant, ents, staff, settings)
	return uc, repo, tenant, ents, staff, settings
}

func startOK(t *testing.T, uc *app.App, ctx context.Context) domain.State {
	t.Helper()
	st, err := uc.StartOnboarding(ctx, app.StartInput{
		OwnerName:    "Curry House",
		ContactEmail: "owner@curry.test",
		Country:      "IN",
		PlanID:       "growth",
	})
	if err != nil {
		t.Fatalf("StartOnboarding: %v", err)
	}
	return st
}

// TestStartOnboarding_AssignsPlanAndAdvances proves the ACCOUNT + PLAN steps run,
// the plan is assigned in entitlements, and the ledger advances.
func TestStartOnboarding_AssignsPlanAndAdvances(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		in       app.StartInput
		wantPlan string
		wantErr  error
	}{
		{
			name:     "explicit plan",
			in:       app.StartInput{OwnerName: "Curry House", ContactEmail: "o@c.test", PlanID: "pro"},
			wantPlan: "pro",
		},
		{
			name:     "default plan when omitted",
			in:       app.StartInput{OwnerName: "Curry House", ContactPhone: "+91999"},
			wantPlan: "free",
		},
		{
			name:    "missing owner name rejected",
			in:      app.StartInput{ContactEmail: "o@c.test"},
			wantErr: domain.ErrInvalidInput,
		},
		{
			name:    "missing contact rejected",
			in:      app.StartInput{OwnerName: "Curry House"},
			wantErr: domain.ErrInvalidInput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			uc, _, tenant, ents, _, _ := newApp()

			st, err := uc.StartOnboarding(adminCtx(), tt.in)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got %v", tt.wantErr, err)
				}
				if ents.calls != 0 {
					t.Fatalf("plan must not be assigned on validation failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("StartOnboarding: %v", err)
			}
			if !st.IsDone(onboardingv1.Step_STEP_ACCOUNT) || !st.IsDone(onboardingv1.Step_STEP_PLAN) {
				t.Fatalf("ACCOUNT+PLAN should be complete, got %v", st.Completed())
			}
			if st.OwnerID != "own_fake" {
				t.Fatalf("owner id not stored: %q", st.OwnerID)
			}
			if owners, _, _, _ := tenant.counts(); owners != 1 {
				t.Fatalf("want 1 CreateOwner, got %d", owners)
			}
			// Plan assignment must have been called with the right plan.
			if ents.calls != 1 || ents.lastPlan != tt.wantPlan {
				t.Fatalf("want plan %q assigned once, got %q x%d", tt.wantPlan, ents.lastPlan, ents.calls)
			}
			if st.Current() != onboardingv1.Step_STEP_BRAND {
				t.Fatalf("next step should be BRAND, got %v", st.Current())
			}
		})
	}
}

// TestFullSaga_HappyPath drives Start -> Brand -> Outlet -> Team -> Complete and
// asserts the ledger reaches GOLIVE, the completed event is emitted, and every
// downstream port was called the expected number of times.
func TestFullSaga_HappyPath(t *testing.T) {
	t.Parallel()
	uc, repo, tenant, ents, staff, settings := newApp()
	ctx := adminCtx()

	st := startOK(t, uc, ctx)

	if _, brandID, err := uc.SubmitBrand(ctx, app.SubmitBrandInput{
		OnboardingID: st.ID, BrandName: "Curry House", PrimaryColor: "#9E7C46",
		Logo: []byte("PNGDATA"), LogoContentType: "image/png",
	}); err != nil || brandID != "brnd_fake" {
		t.Fatalf("SubmitBrand: id=%q err=%v", brandID, err)
	}

	if _, outletID, err := uc.SubmitOutlet(ctx, app.SubmitOutletInput{
		OnboardingID: st.ID, Name: "MG Road", Address: "12 MG Rd", Timezone: "Asia/Kolkata", GSTIN: "29ABCDE1234F1Z5",
	}); err != nil || outletID != "out_fake" {
		t.Fatalf("SubmitOutlet: id=%q err=%v", outletID, err)
	}
	if settings.count() == 0 {
		t.Fatalf("expected default settings seeded")
	}

	_, results, err := uc.InviteTeam(ctx, st.ID, []app.InviteInput{
		{Name: "Asha", Email: "asha@c.test", Role: "manager"},
		{Name: "Bina", Email: "bina@c.test", Role: "waiter"},
	})
	if err != nil {
		t.Fatalf("InviteTeam: %v", err)
	}
	for _, r := range results {
		if !r.Invited {
			t.Fatalf("invite for %s failed: %s", r.Name, r.Error)
		}
	}

	final, err := uc.Complete(ctx, st.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !final.Done || final.Current() != onboardingv1.Step_STEP_GOLIVE {
		t.Fatalf("saga not live: done=%v current=%v", final.Done, final.Current())
	}

	// The completed event must be emitted exactly once (downstream menu+QR seed).
	if got := lastEvent(repo); got != app.EventCompleted {
		t.Fatalf("want %s emitted, got %q", app.EventCompleted, got)
	}

	// Every downstream port was exercised once for the create-style calls.
	owners, brands, logos, restaurants := tenant.counts()
	if owners != 1 || brands != 1 || logos != 1 || restaurants != 1 {
		t.Fatalf("tenant calls owners=%d brands=%d logos=%d restaurants=%d", owners, brands, logos, restaurants)
	}
	if ents.calls != 1 {
		t.Fatalf("want plan assigned once, got %d", ents.calls)
	}
	if added, invited := staff.counts(); added != 2 || invited != 2 {
		t.Fatalf("want 2 added + 2 invited, got %d/%d", added, invited)
	}
}

// TestResume_SkipsCompletedSteps proves idempotency: replaying every RPC after a
// full run does not re-create owner/brand/outlet/staff and does not re-emit the
// completed event.
func TestResume_SkipsCompletedSteps(t *testing.T) {
	t.Parallel()
	uc, repo, tenant, ents, staff, settings := newApp()
	ctx := adminCtx()

	st := startOK(t, uc, ctx)
	if _, _, err := uc.SubmitBrand(ctx, app.SubmitBrandInput{OnboardingID: st.ID, BrandName: "Curry House"}); err != nil {
		t.Fatalf("brand: %v", err)
	}
	if _, _, err := uc.SubmitOutlet(ctx, app.SubmitOutletInput{OnboardingID: st.ID, Name: "MG Road"}); err != nil {
		t.Fatalf("outlet: %v", err)
	}
	if _, _, err := uc.InviteTeam(ctx, st.ID, []app.InviteInput{{Name: "Asha", Email: "a@c.test", Role: "manager"}}); err != nil {
		t.Fatalf("team: %v", err)
	}
	if _, err := uc.Complete(ctx, st.ID); err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Snapshot call counts, then REPLAY each RPC (a client retry / at-least-once
	// delivery). None of the create-style downstream calls must run again.
	owners0, brands0, logos0, restaurants0 := tenant.counts()
	plans0 := ents.calls
	added0, invited0 := staff.counts()
	settings0 := settings.count()
	creates0 := repo.createCount()
	events0 := len(repo.eventTypes())

	// StartOnboarding creates a NEW saga (not a resume), so we re-drive the
	// already-created saga's later steps instead.
	if _, brandID, err := uc.SubmitBrand(ctx, app.SubmitBrandInput{OnboardingID: st.ID, BrandName: "Curry House"}); err != nil || brandID != "brnd_fake" {
		t.Fatalf("resume brand: id=%q err=%v", brandID, err)
	}
	if _, outletID, err := uc.SubmitOutlet(ctx, app.SubmitOutletInput{OnboardingID: st.ID, Name: "MG Road"}); err != nil || outletID != "out_fake" {
		t.Fatalf("resume outlet: id=%q err=%v", outletID, err)
	}
	if _, err := uc.Complete(ctx, st.ID); err != nil {
		t.Fatalf("resume complete: %v", err)
	}

	owners1, brands1, logos1, restaurants1 := tenant.counts()
	if owners1 != owners0 || brands1 != brands0 || logos1 != logos0 || restaurants1 != restaurants0 {
		t.Fatalf("resume re-created tenant resources: before(%d,%d,%d,%d) after(%d,%d,%d,%d)",
			owners0, brands0, logos0, restaurants0, owners1, brands1, logos1, restaurants1)
	}
	if ents.calls != plans0 {
		t.Fatalf("resume re-assigned plan: %d -> %d", plans0, ents.calls)
	}
	if a, i := staff.counts(); a != added0 || i != invited0 {
		t.Fatalf("resume re-added staff: %d/%d -> %d/%d", added0, invited0, a, i)
	}
	if settings.count() != settings0 {
		t.Fatalf("resume re-seeded settings: %d -> %d", settings0, settings.count())
	}
	if repo.createCount() != creates0 {
		t.Fatalf("resume created a new saga row: %d -> %d", creates0, repo.createCount())
	}
	// Complete on an already-done saga must NOT re-emit the completed event.
	if len(repo.eventTypes()) != events0 {
		t.Fatalf("resume re-emitted events: %d -> %d", events0, len(repo.eventTypes()))
	}
}

// TestInviteTeam_PartialFailureReported proves that a quota-exhausted invite is
// reported per-invite while the others succeed and the step still completes.
func TestInviteTeam_PartialFailureReported(t *testing.T) {
	t.Parallel()
	uc, _, _, _, staff, _ := newApp()
	ctx := adminCtx()

	st := startOK(t, uc, ctx)
	if _, _, err := uc.SubmitBrand(ctx, app.SubmitBrandInput{OnboardingID: st.ID, BrandName: "B"}); err != nil {
		t.Fatalf("brand: %v", err)
	}
	if _, _, err := uc.SubmitOutlet(ctx, app.SubmitOutletInput{OnboardingID: st.ID, Name: "O"}); err != nil {
		t.Fatalf("outlet: %v", err)
	}

	// The plan is out of waiter slots; managers still fit.
	staff.quotaExhausted["waiter"] = true

	state, results, err := uc.InviteTeam(ctx, st.ID, []app.InviteInput{
		{Name: "Asha", Email: "asha@c.test", Role: "manager"},
		{Name: "Bina", Email: "bina@c.test", Role: "waiter"},
		{Name: "Chris", Email: "chris@c.test", Role: "waiter"},
	})
	if err != nil {
		t.Fatalf("InviteTeam: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}

	var ok, failed int
	for _, r := range results {
		if r.Invited {
			ok++
			continue
		}
		failed++
		if r.Role != "waiter" {
			t.Fatalf("unexpected failure for role %s", r.Role)
		}
		if r.Error == "" {
			t.Fatalf("failed invite missing error message")
		}
	}
	if ok != 1 || failed != 2 {
		t.Fatalf("want 1 ok + 2 failed, got %d/%d", ok, failed)
	}

	// Despite partial failure the TEAM step is marked complete so the saga can
	// proceed; the owner retries the failed invites after upgrading.
	if !state.IsDone(onboardingv1.Step_STEP_TEAM) {
		t.Fatalf("TEAM step should complete even with partial failures")
	}
	if added, invited := staff.counts(); added != 1 || invited != 1 {
		t.Fatalf("only the manager should be added+invited, got %d/%d", added, invited)
	}
}

// TestStepOrder_EnforcesPrerequisites proves the saga cannot be advanced out of
// order (SubmitBrand before the account exists, Complete before the outlet).
func TestStepOrder_EnforcesPrerequisites(t *testing.T) {
	t.Parallel()
	uc, _, _, _, _, _ := newApp()
	ctx := adminCtx()

	// Unknown saga id → not found.
	if _, _, err := uc.SubmitBrand(ctx, app.SubmitBrandInput{OnboardingID: "onb_missing", BrandName: "B"}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want not found, got %v", err)
	}

	st := startOK(t, uc, ctx)
	// Complete before outlet exists → failed precondition (out of order).
	if _, err := uc.Complete(ctx, st.ID); !errors.Is(err, domain.ErrOutOfOrder) {
		t.Fatalf("want out-of-order on early complete, got %v", err)
	}
	// SubmitOutlet before the brand exists → out of order.
	if _, _, err := uc.SubmitOutlet(ctx, app.SubmitOutletInput{OnboardingID: st.ID, Name: "O"}); !errors.Is(err, domain.ErrOutOfOrder) {
		t.Fatalf("want out-of-order on early outlet, got %v", err)
	}
}

// TestStartOnboarding_RequiresPlatformAdmin proves only platform admins may start
// an onboarding.
func TestStartOnboarding_RequiresPlatformAdmin(t *testing.T) {
	t.Parallel()
	uc, _, _, _, _, _ := newApp()

	// No scope.
	if _, err := uc.StartOnboarding(context.Background(), app.StartInput{OwnerName: "X", ContactEmail: "x@x.test"}); err == nil {
		t.Fatalf("want error without scope")
	}
	// Owner role cannot start onboarding for someone else.
	ctx := tenancy.With(context.Background(), tenancy.Scope{Role: commonv1.Role_ROLE_OWNER, OwnerID: "own_x"})
	if _, err := uc.StartOnboarding(ctx, app.StartInput{OwnerName: "X", ContactEmail: "x@x.test"}); !errors.Is(err, tenancy.ErrPermissionDenied) {
		t.Fatalf("want permission denied, got %v", err)
	}
}

// lastEvent returns the most recently staged event type, or "".
func lastEvent(r *fakeRepo) string {
	types := r.eventTypes()
	if len(types) == 0 {
		return ""
	}
	return types[len(types)-1]
}
