package app_test

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/staff/internal/app"
	"github.com/restorna/platform/services/staff/internal/domain"
)

const (
	ownerID = "own_01hxowner000000000000000"
	restA   = "out_01hxrestaurantaaaaaaaaaaa"
	restB   = "out_01hxrestaurantbbbbbbbbbbb"
)

// ownerCtx returns a context with an owner-role tenancy scope for restA.
func ownerCtx() context.Context {
	return tenancy.With(context.Background(), tenancy.Scope{
		OwnerID:      ownerID,
		RestaurantID: restA,
		Role:         commonv1.Role_ROLE_OWNER,
	})
}

func TestAddStaff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limits    map[string]int64
		preadd    int // how many waiters to add first
		role      commonv1.Role
		wantErr   error // sentinel to errors.Is against; nil = success
		wantQuota bool  // expect an ErrQuotaExceeded with an upgrade hint
	}{
		{
			name:   "within limit reserves and persists",
			limits: map[string]int64{"staff.waiter": 2},
			role:   commonv1.Role_ROLE_WAITER,
		},
		{
			name:      "over limit blocks with quota exceeded",
			limits:    map[string]int64{"staff.waiter": 1},
			preadd:    1,
			role:      commonv1.Role_ROLE_WAITER,
			wantQuota: true,
		},
		{
			name:    "invalid role rejected",
			limits:  map[string]int64{},
			role:    commonv1.Role_ROLE_OWNER,
			wantErr: domain.ErrInvalidRole,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			repo := newFakeRepo()
			ent := newFakeEnt(tt.limits, "Upgrade to Growth for more waiters")
			uc := app.New(repo, ent, &fakeSender{})
			ctx := ownerCtx()

			for i := 0; i < tt.preadd; i++ {
				if _, err := uc.AddStaff(ctx, restA, "Pre", "pre@x.com", "", commonv1.Role_ROLE_WAITER); err != nil {
					t.Fatalf("preadd: %v", err)
				}
			}

			m, err := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", tt.role)

			if tt.wantQuota {
				var qe app.ErrQuotaExceeded
				if !errors.As(err, &qe) {
					t.Fatalf("want ErrQuotaExceeded, got %v", err)
				}
				if qe.UpgradeHint == "" {
					t.Fatalf("want upgrade hint on quota error")
				}
				return
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got %v", tt.wantErr, err)
				}
				return
			}

			if err != nil {
				t.Fatalf("AddStaff: %v", err)
			}
			if m.ID == "" || m.Role != tt.role || !m.Active {
				t.Fatalf("bad member: %+v", m)
			}
			if got := ent.used(domain.QuotaKeyFor(tt.role)); got != 1 {
				t.Fatalf("want 1 reserved, got %d", got)
			}
			if types := repo.eventTypes(); len(types) == 0 || types[len(types)-1] != app.EventMemberAdded {
				t.Fatalf("want member.added event, got %v", types)
			}
		})
	}
}

func TestAddStaff_PersistFailureReleasesReservation(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	repo.failNext = errBoom
	ent := newFakeEnt(map[string]int64{"staff.waiter": 5}, "hint")
	uc := app.New(repo, ent, &fakeSender{})

	_, err := uc.AddStaff(ownerCtx(), restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)
	if err == nil {
		t.Fatalf("want persist error")
	}
	if got := ent.used("staff.waiter"); got != 0 {
		t.Fatalf("reservation leaked: used=%d, want 0", got)
	}
}

func TestSetStaffActive_ReleaseAndReReserve(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{"staff.waiter": 1}, "Upgrade")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	m, err := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if ent.used("staff.waiter") != 1 {
		t.Fatalf("want 1 reserved after add")
	}

	// Deactivate releases the slot.
	if _, err := uc.SetStaffActive(ctx, m.ID, false); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if got := ent.used("staff.waiter"); got != 0 {
		t.Fatalf("want 0 reserved after deactivate, got %d", got)
	}
	if types := repo.eventTypes(); types[len(types)-1] != app.EventMemberDeactivated {
		t.Fatalf("want deactivated event, got %v", types)
	}

	// Now a second waiter fits (limit 1, slot freed).
	if _, err := uc.AddStaff(ctx, restA, "Bina", "bina@x.com", "", commonv1.Role_ROLE_WAITER); err != nil {
		t.Fatalf("second add should fit after release: %v", err)
	}

	// Reactivating the first is now blocked (limit reached again).
	_, err = uc.SetStaffActive(ctx, m.ID, true)
	var qe app.ErrQuotaExceeded
	if !errors.As(err, &qe) {
		t.Fatalf("want quota exceeded on reactivate, got %v", err)
	}
}

func TestSetStaffActive_DeactivateIdempotent(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{"staff.waiter": 3}, "")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	m, _ := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)
	if _, err := uc.SetStaffActive(ctx, m.ID, false); err != nil {
		t.Fatalf("first deactivate: %v", err)
	}
	if _, err := uc.SetStaffActive(ctx, m.ID, false); err != nil {
		t.Fatalf("second deactivate should be no-op: %v", err)
	}
	if got := ent.used("staff.waiter"); got != 0 {
		t.Fatalf("want 0 reserved, got %d", got)
	}
}

func TestChangeRole_SwapsReservations(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{
		"staff.waiter":  1,
		"staff.cashier": 1,
	}, "Upgrade")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	m, err := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)
	if err != nil {
		t.Fatalf("add: %v", err)
	}

	got, err := uc.ChangeRole(ctx, m.ID, commonv1.Role_ROLE_CASHIER)
	if err != nil {
		t.Fatalf("change role: %v", err)
	}
	if got.Role != commonv1.Role_ROLE_CASHIER {
		t.Fatalf("want cashier, got %v", got.Role)
	}
	if w := ent.used("staff.waiter"); w != 0 {
		t.Fatalf("want waiter slot released, got %d", w)
	}
	if c := ent.used("staff.cashier"); c != 1 {
		t.Fatalf("want cashier slot reserved, got %d", c)
	}
	if types := repo.eventTypes(); types[len(types)-1] != app.EventRoleChanged {
		t.Fatalf("want role_changed event, got %v", types)
	}

	// A new waiter now fits because the old role's slot was freed.
	if _, err := uc.AddStaff(ctx, restA, "Bina", "bina@x.com", "", commonv1.Role_ROLE_WAITER); err != nil {
		t.Fatalf("waiter should fit after swap: %v", err)
	}
}

func TestChangeRole_NewRoleFullRollsBack(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{
		"staff.waiter":  5,
		"staff.cashier": 0, // no cashier slots
	}, "Upgrade for cashiers")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	m, _ := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)

	_, err := uc.ChangeRole(ctx, m.ID, commonv1.Role_ROLE_CASHIER)
	var qe app.ErrQuotaExceeded
	if !errors.As(err, &qe) {
		t.Fatalf("want quota exceeded, got %v", err)
	}
	// Original waiter reservation must remain intact.
	if got := ent.used("staff.waiter"); got != 1 {
		t.Fatalf("waiter slot must remain reserved, got %d", got)
	}
	cur, _ := repo.Get(ctx, ownerID, m.ID)
	if cur.Role != commonv1.Role_ROLE_WAITER {
		t.Fatalf("role must be unchanged after rollback, got %v", cur.Role)
	}
}

func TestListStaff_ByRestaurant(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{"staff.waiter": -1}, "")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	for i := 0; i < 3; i++ {
		if _, err := uc.AddStaff(ctx, restA, "A", "a@x.com", "", commonv1.Role_ROLE_WAITER); err != nil {
			t.Fatalf("add A: %v", err)
		}
	}
	if _, err := uc.AddStaff(ctx, restB, "B", "b@x.com", "", commonv1.Role_ROLE_WAITER); err != nil {
		t.Fatalf("add B: %v", err)
	}

	members, page, err := uc.ListStaff(ctx, restA, &commonv1.PageRequest{PageSize: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(members) != 3 || page.Total != 3 {
		t.Fatalf("want 3 in restA, got %d (total %d)", len(members), page.Total)
	}
	for _, m := range members {
		if m.RestaurantID != restA {
			t.Fatalf("list leaked restaurant %s", m.RestaurantID)
		}
	}
}

func TestListStaff_Paging(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{"staff.waiter": -1}, "")
	uc := app.New(repo, ent, &fakeSender{})
	ctx := ownerCtx()

	for i := 0; i < 5; i++ {
		uc.AddStaff(ctx, restA, "A", "a@x.com", "", commonv1.Role_ROLE_WAITER)
	}

	first, page, err := uc.ListStaff(ctx, restA, &commonv1.PageRequest{PageSize: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 2 || page.NextPageToken == "" {
		t.Fatalf("want 2 + next token, got %d token=%q", len(first), page.NextPageToken)
	}
	second, page2, err := uc.ListStaff(ctx, restA, &commonv1.PageRequest{PageSize: 2, PageToken: page.NextPageToken})
	if err != nil {
		t.Fatalf("list2: %v", err)
	}
	if len(second) != 2 || page2.NextPageToken == "" {
		t.Fatalf("want 2 more + token, got %d token=%q", len(second), page2.NextPageToken)
	}
	if first[0].ID == second[0].ID {
		t.Fatalf("paging overlapped")
	}
}

func TestInviteStaff_SendsAndEmits(t *testing.T) {
	t.Parallel()
	repo := newFakeRepo()
	ent := newFakeEnt(map[string]int64{"staff.waiter": 5}, "")
	sender := &fakeSender{}
	uc := app.New(repo, ent, sender)
	ctx := ownerCtx()

	m, _ := uc.AddStaff(ctx, restA, "Asha", "asha@x.com", "", commonv1.Role_ROLE_WAITER)

	inviteID, err := uc.InviteStaff(ctx, m.ID)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if inviteID == "" {
		t.Fatalf("want invite id")
	}
	if sender.count() != 1 {
		t.Fatalf("want 1 invite sent, got %d", sender.count())
	}
	types := repo.eventTypes()
	if types[len(types)-1] != app.EventInvited {
		t.Fatalf("want invited event, got %v", types)
	}
}

func TestInviteStaff_NotFound(t *testing.T) {
	t.Parallel()
	uc := app.New(newFakeRepo(), newFakeEnt(nil, ""), &fakeSender{})
	if _, err := uc.InviteStaff(ownerCtx(), "stf_missing"); !errors.Is(err, domain.ErrStaffNotFound) {
		t.Fatalf("want not found, got %v", err)
	}
}

func TestAddStaff_RequiresScope(t *testing.T) {
	t.Parallel()
	uc := app.New(newFakeRepo(), newFakeEnt(map[string]int64{"staff.waiter": 5}, ""), &fakeSender{})
	// No tenancy scope in context.
	if _, err := uc.AddStaff(context.Background(), restA, "X", "x@x.com", "", commonv1.Role_ROLE_WAITER); err == nil {
		t.Fatalf("want error without scope")
	}
	// Wrong role (waiter cannot add staff).
	ctx := tenancy.With(context.Background(), tenancy.Scope{OwnerID: ownerID, Role: commonv1.Role_ROLE_WAITER})
	if _, err := uc.AddStaff(ctx, restA, "X", "x@x.com", "", commonv1.Role_ROLE_WAITER); !errors.Is(err, tenancy.ErrPermissionDenied) {
		t.Fatalf("want permission denied, got %v", err)
	}
}
