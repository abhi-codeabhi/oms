package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/servicerequests/internal/domain"
)

func ts() time.Time { return time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC) }

func TestRaise_ValidationAndInitialState(t *testing.T) {
	tests := []struct {
		name       string
		typ        domain.Type
		table      int32
		assignedTo string
		wantErr    bool
		wantState  domain.State
	}{
		{"assigned starts assigned", domain.TypeCall, 7, "stf_1", false, domain.StateAssigned},
		{"unassigned starts escalated", domain.TypeWater, 3, "", false, domain.StateEscalated},
		{"bill ok", domain.TypeBill, 5, "stf_2", false, domain.StateAssigned},
		{"bad type", domain.Type("food"), 1, "", true, ""},
		{"zero table", domain.TypeCall, 0, "", true, ""},
		{"negative table", domain.TypeCall, -2, "", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := domain.Raise(tc.typ, tc.table, tc.assignedTo, ts())
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixRequest, r.ID) {
				t.Fatalf("request id %q not a valid req_ ULID", r.ID)
			}
			if r.State != tc.wantState {
				t.Fatalf("state = %q, want %q", r.State, tc.wantState)
			}
			if !r.AckedAt.IsZero() {
				t.Fatal("fresh request should have zero AckedAt")
			}
		})
	}
}

func TestCanRaise_Cooldown(t *testing.T) {
	now := ts()
	cooldown := 60 * time.Second
	tests := []struct {
		name    string
		lastAck time.Time
		want    bool
	}{
		{"never acknowledged", time.Time{}, true},
		{"inside cooldown", now.Add(-30 * time.Second), false},
		{"exactly at boundary", now.Add(-60 * time.Second), true},
		{"past cooldown", now.Add(-90 * time.Second), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.CanRaise(tc.lastAck, now, cooldown); got != tc.want {
				t.Fatalf("CanRaise = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldEscalate(t *testing.T) {
	now := ts()
	threshold := 30 * time.Second
	assigned := func(createdAgo time.Duration) domain.Request {
		return domain.Request{State: domain.StateAssigned, CreatedAt: now.Add(-createdAgo)}
	}
	tests := []struct {
		name string
		req  domain.Request
		want bool
	}{
		{"assigned and overdue", assigned(45 * time.Second), true},
		{"assigned at boundary", assigned(30 * time.Second), true},
		{"assigned but fresh", assigned(10 * time.Second), false},
		{"already escalated never re-escalates", domain.Request{State: domain.StateEscalated, CreatedAt: now.Add(-time.Hour)}, false},
		{"done never escalates", domain.Request{State: domain.StateDone, CreatedAt: now.Add(-time.Hour)}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.ShouldEscalate(tc.req, now, threshold); got != tc.want {
				t.Fatalf("ShouldEscalate = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEscalateAndAcknowledge(t *testing.T) {
	r, _ := domain.Raise(domain.TypeCall, 4, "stf_1", ts())

	r.Escalate()
	if r.State != domain.StateEscalated {
		t.Fatalf("after Escalate state = %q", r.State)
	}

	ackAt := ts().Add(2 * time.Minute)
	r.Acknowledge(ackAt)
	if r.State != domain.StateDone {
		t.Fatalf("after Acknowledge state = %q, want done", r.State)
	}
	if !r.AckedAt.Equal(ackAt) {
		t.Fatalf("AckedAt = %v, want %v", r.AckedAt, ackAt)
	}
	if r.IsOpen() {
		t.Fatal("done request should not be open")
	}
}

func TestOpenOnly_ExcludesDone(t *testing.T) {
	mk := func(state domain.State) domain.Request { return domain.Request{ID: ids.New(domain.PrefixRequest), State: state} }
	in := []domain.Request{
		mk(domain.StateAssigned),
		mk(domain.StateDone),
		mk(domain.StateEscalated),
		mk(domain.StateDone),
	}
	open := domain.OpenOnly(in)
	if len(open) != 2 {
		t.Fatalf("want 2 open requests, got %d", len(open))
	}
	for _, r := range open {
		if r.State == domain.StateDone {
			t.Fatal("OpenOnly must exclude done requests")
		}
	}
}
