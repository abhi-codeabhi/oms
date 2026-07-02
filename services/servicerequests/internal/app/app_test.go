package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/servicerequests/internal/app"
	"github.com/restorna/platform/services/servicerequests/internal/domain"
)

const rid = "out_01hx0000000000000000000000"

// settings used across tests: 60s cooldown, 30s escalation.
var stdSettings = fakeSettings{cooldown: 60 * time.Second, escalation: 30 * time.Second}

func clockAt(t time.Time) app.Now { return func() time.Time { return t } }

func baseTime() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

func TestRaise_CreatesAndEmitsRaised(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(baseTime()))

	r, err := a.Raise(context.Background(), rid, app.RaiseInput{Type: "bill", Table: 7, AssignedTo: "stf_1"})
	if err != nil {
		t.Fatalf("Raise: %v", err)
	}
	if !ids.Valid(domain.PrefixRequest, r.ID) {
		t.Fatalf("request id %q invalid", r.ID)
	}
	if r.State != domain.StateAssigned {
		t.Fatalf("assigned raise should start assigned, got %q", r.State)
	}
	all, _ := repo.List(context.Background(), rid)
	if len(all) != 1 {
		t.Fatalf("want 1 persisted request, got %d", len(all))
	}
	if n := countEvents(repo, app.EventRaised); n != 1 {
		t.Fatalf("Raise should emit exactly 1 raised event, got %d", n)
	}
}

func TestRaise_RejectedWithinCooldown(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(now))

	// First raise for table 5 / water, then acknowledge it (records the cooldown).
	r, err := a.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 5, AssignedTo: "stf_1"})
	if err != nil {
		t.Fatalf("first raise: %v", err)
	}
	if _, err := a.Acknowledge(context.Background(), rid, r.ID); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// Re-raise the SAME table+type at the same instant -> inside the 60s cooldown.
	_, err = a.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 5, AssignedTo: "stf_1"})
	if !errors.Is(err, domain.ErrCooldown) {
		t.Fatalf("re-raise within cooldown should be ErrCooldown, got %v", err)
	}

	// A different type at the same table is NOT in cooldown.
	if _, err := a.Raise(context.Background(), rid, app.RaiseInput{Type: "bill", Table: 5, AssignedTo: "stf_1"}); err != nil {
		t.Fatalf("different type should not be rate-limited: %v", err)
	}

	// After the cooldown elapses the same table+type may raise again.
	later := app.New(repo, stdSettings, clockAt(now.Add(61*time.Second)))
	if _, err := later.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 5, AssignedTo: "stf_1"}); err != nil {
		t.Fatalf("raise after cooldown should succeed: %v", err)
	}
}

func TestRaise_InvalidInput(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(baseTime()))
	cases := []app.RaiseInput{
		{Type: "food", Table: 1},  // bad type
		{Type: "call", Table: 0},  // bad table
		{Type: "call", Table: -3}, // negative table
	}
	for i, in := range cases {
		if _, err := a.Raise(context.Background(), rid, in); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}

func TestRaise_DefaultsWhenSettingsUnavailable(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	// settings errors -> app falls back to DefaultCooldown (60s).
	a := app.New(repo, fakeSettings{err: errors.New("settings down")}, clockAt(now))

	r, _ := a.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 2, AssignedTo: "stf_1"})
	a.Acknowledge(context.Background(), rid, r.ID)

	// 30s later: still inside the default 60s cooldown -> rejected.
	mid := app.New(repo, fakeSettings{err: errors.New("settings down")}, clockAt(now.Add(30*time.Second)))
	if _, err := mid.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 2}); !errors.Is(err, domain.ErrCooldown) {
		t.Fatalf("default cooldown should still rate-limit at 30s, got %v", err)
	}
}

func TestAcknowledge_SetsDoneAndCooldown(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(now))

	r, _ := a.Raise(context.Background(), rid, app.RaiseInput{Type: "call", Table: 9, AssignedTo: "stf_1"})
	ack, err := a.Acknowledge(context.Background(), rid, r.ID)
	if err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if ack.State != domain.StateDone {
		t.Fatalf("acknowledged request should be done, got %q", ack.State)
	}
	if !ack.AckedAt.Equal(now) {
		t.Fatalf("AckedAt = %v, want %v", ack.AckedAt, now)
	}
	// The cooldown anchor for table 9 / call must now be set.
	lastAck, _ := repo.LastAck(context.Background(), rid, 9, domain.TypeCall)
	if !lastAck.Equal(now) {
		t.Fatalf("cooldown not recorded for table+type, got %v", lastAck)
	}
}

func TestAcknowledge_NotFound(t *testing.T) {
	a := app.New(newFakeRepo(), stdSettings, clockAt(baseTime()))
	if _, err := a.Acknowledge(context.Background(), rid, "req_missing"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestListOpen_ExcludesDone(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(now))

	// Two open requests + one acknowledged (done).
	a.Raise(context.Background(), rid, app.RaiseInput{Type: "call", Table: 1, AssignedTo: "stf_1"})
	open2, _ := a.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 2}) // unassigned -> escalated
	done, _ := a.Raise(context.Background(), rid, app.RaiseInput{Type: "cutlery", Table: 3, AssignedTo: "stf_1"})
	a.Acknowledge(context.Background(), rid, done.ID)

	openList, err := a.ListOpen(context.Background(), rid)
	if err != nil {
		t.Fatalf("ListOpen: %v", err)
	}
	if len(openList) != 2 {
		t.Fatalf("want 2 open requests, got %d", len(openList))
	}
	for _, r := range openList {
		if r.State == domain.StateDone {
			t.Fatal("ListOpen must exclude done requests")
		}
		if r.ID == done.ID {
			t.Fatal("done request must not appear in ListOpen")
		}
	}
	// open2 (unassigned) should be present and escalated.
	var sawEscalated bool
	for _, r := range openList {
		if r.ID == open2.ID && r.State == domain.StateEscalated {
			sawEscalated = true
		}
	}
	if !sawEscalated {
		t.Fatal("unassigned raise should be open and escalated")
	}
}

func TestEscalateDue_FlipsOverdueAssigned(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	a := app.New(repo, stdSettings, clockAt(now))

	// Old assigned request (created 45s ago, threshold 30s) -> should escalate.
	old := app.New(repo, stdSettings, clockAt(now.Add(-45*time.Second)))
	overdue, _ := old.Raise(context.Background(), rid, app.RaiseInput{Type: "call", Table: 1, AssignedTo: "stf_1"})

	// Fresh assigned request (just now) -> should NOT escalate.
	fresh, _ := a.Raise(context.Background(), rid, app.RaiseInput{Type: "water", Table: 2, AssignedTo: "stf_1"})

	escalated, err := a.EscalateDue(context.Background(), rid, now)
	if err != nil {
		t.Fatalf("EscalateDue: %v", err)
	}
	if len(escalated) != 1 || escalated[0].ID != overdue.ID {
		t.Fatalf("only the overdue request should escalate, got %d", len(escalated))
	}
	if escalated[0].State != domain.StateEscalated {
		t.Fatalf("escalated request state = %q", escalated[0].State)
	}
	if n := countEvents(repo, app.EventEscalated); n != 1 {
		t.Fatalf("EscalateDue should emit 1 escalated event, got %d", n)
	}

	// The fresh one must remain assigned.
	all, _ := repo.List(context.Background(), rid)
	for _, r := range all {
		if r.ID == fresh.ID && r.State != domain.StateAssigned {
			t.Fatalf("fresh request should stay assigned, got %q", r.State)
		}
	}

	// Running again at the same instant escalates nothing more (idempotent sweep).
	again, _ := a.EscalateDue(context.Background(), rid, now)
	if len(again) != 0 {
		t.Fatalf("re-running EscalateDue should escalate nothing, got %d", len(again))
	}
	if n := countEvents(repo, app.EventEscalated); n != 1 {
		t.Fatalf("re-run should not re-emit, got %d escalated events", n)
	}
}

func TestEscalateDue_UsesAppClockWhenNowZero(t *testing.T) {
	now := baseTime()
	repo := newFakeRepo()
	// Create the request 45s before "now".
	old := app.New(repo, stdSettings, clockAt(now.Add(-45*time.Second)))
	overdue, _ := old.Raise(context.Background(), rid, app.RaiseInput{Type: "call", Table: 1, AssignedTo: "stf_1"})

	// App clock is `now`; passing a zero time should fall back to it.
	a := app.New(repo, stdSettings, clockAt(now))
	escalated, err := a.EscalateDue(context.Background(), rid, time.Time{})
	if err != nil {
		t.Fatalf("EscalateDue: %v", err)
	}
	if len(escalated) != 1 || escalated[0].ID != overdue.ID {
		t.Fatalf("zero now should use the app clock and escalate the overdue request, got %d", len(escalated))
	}
}
