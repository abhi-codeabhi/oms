package domain_test

import (
	"errors"
	"testing"
	"time"

	onboardingv1 "github.com/restorna/platform/gen/go/restorna/onboarding/v1"
	"github.com/restorna/platform/services/onboarding/internal/domain"
)

// TestStepMachine_CompleteAndCurrent table-tests the step ledger: completing
// steps advances Current() through the canonical order and IsDone reports
// membership for idempotency checks.
func TestStepMachine_CompleteAndCurrent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		complete    []onboardingv1.Step
		wantCurrent onboardingv1.Step
		wantDone    bool
	}{
		{
			name:        "fresh starts at ACCOUNT",
			complete:    nil,
			wantCurrent: onboardingv1.Step_STEP_ACCOUNT,
		},
		{
			name:        "account+plan -> brand",
			complete:    []onboardingv1.Step{onboardingv1.Step_STEP_ACCOUNT, onboardingv1.Step_STEP_PLAN},
			wantCurrent: onboardingv1.Step_STEP_BRAND,
		},
		{
			name: "through settings -> team",
			complete: []onboardingv1.Step{
				onboardingv1.Step_STEP_ACCOUNT, onboardingv1.Step_STEP_PLAN,
				onboardingv1.Step_STEP_BRAND, onboardingv1.Step_STEP_OUTLET,
				onboardingv1.Step_STEP_SETTINGS,
			},
			wantCurrent: onboardingv1.Step_STEP_TEAM,
		},
		{
			name: "golive marks done",
			complete: []onboardingv1.Step{
				onboardingv1.Step_STEP_ACCOUNT, onboardingv1.Step_STEP_PLAN,
				onboardingv1.Step_STEP_BRAND, onboardingv1.Step_STEP_OUTLET,
				onboardingv1.Step_STEP_SETTINGS, onboardingv1.Step_STEP_TEAM,
				onboardingv1.Step_STEP_GOLIVE,
			},
			wantCurrent: onboardingv1.Step_STEP_GOLIVE,
			wantDone:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			st := domain.New("onb_test")
			for _, s := range tt.complete {
				st.Complete(s)
			}
			if got := st.Current(); got != tt.wantCurrent {
				t.Fatalf("Current() = %v, want %v", got, tt.wantCurrent)
			}
			if st.Done != tt.wantDone {
				t.Fatalf("Done = %v, want %v", st.Done, tt.wantDone)
			}
			for _, s := range tt.complete {
				if !st.IsDone(s) {
					t.Fatalf("IsDone(%v) = false after completing it", s)
				}
			}
		})
	}
}

// TestComplete_Idempotent proves completing the same step twice is a safe no-op
// (the property the saga relies on for at-least-once retries).
func TestComplete_Idempotent(t *testing.T) {
	t.Parallel()
	st := domain.New("onb_test")
	st.Complete(onboardingv1.Step_STEP_ACCOUNT)
	first := st.Completed()
	st.Complete(onboardingv1.Step_STEP_ACCOUNT)
	second := st.Completed()
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("repeated Complete changed the ledger: %v -> %v", first, second)
	}
}

// TestRequire_Prerequisites checks the out-of-order guard.
func TestRequire_Prerequisites(t *testing.T) {
	t.Parallel()
	st := domain.New("onb_test")
	if err := st.Require(onboardingv1.Step_STEP_ACCOUNT); !errors.Is(err, domain.ErrOutOfOrder) {
		t.Fatalf("want out-of-order before account, got %v", err)
	}
	st.Complete(onboardingv1.Step_STEP_ACCOUNT)
	if err := st.Require(onboardingv1.Step_STEP_ACCOUNT); err != nil {
		t.Fatalf("want nil after account done, got %v", err)
	}
}

// TestCanComplete gates the GOLIVE transition on OUTLET + SETTINGS and rejects a
// second completion.
func TestCanComplete(t *testing.T) {
	t.Parallel()
	st := domain.New("onb_test")
	if err := st.CanComplete(); !errors.Is(err, domain.ErrOutOfOrder) {
		t.Fatalf("want out-of-order before outlet, got %v", err)
	}
	st.Complete(onboardingv1.Step_STEP_OUTLET)
	st.Complete(onboardingv1.Step_STEP_SETTINGS)
	if err := st.CanComplete(); err != nil {
		t.Fatalf("want nil once outlet+settings done, got %v", err)
	}
	st.Complete(onboardingv1.Step_STEP_GOLIVE)
	if err := st.CanComplete(); !errors.Is(err, domain.ErrAlreadyDone) {
		t.Fatalf("want already-done after golive, got %v", err)
	}
}

// TestRebuild round-trips persisted fields back into a usable ledger.
func TestRebuild(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	st := domain.Rebuild("onb_1", "own_1", "usr_1", "brnd_1", "https://logo", "out_1", "growth",
		[]onboardingv1.Step{onboardingv1.Step_STEP_ACCOUNT, onboardingv1.Step_STEP_PLAN, onboardingv1.Step_STEP_BRAND},
		false, now, now)
	if st.OwnerID != "own_1" || st.BrandID != "brnd_1" || st.OutletID != "out_1" || st.PlanID != "growth" {
		t.Fatalf("rebuild lost fields: %+v", st)
	}
	if !st.IsDone(onboardingv1.Step_STEP_BRAND) {
		t.Fatalf("rebuild lost completed steps")
	}
	if st.Current() != onboardingv1.Step_STEP_OUTLET {
		t.Fatalf("Current() = %v, want OUTLET", st.Current())
	}
}
