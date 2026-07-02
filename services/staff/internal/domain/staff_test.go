package domain_test

import (
	"errors"
	"testing"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/services/staff/internal/domain"
)

var scope = domain.Scope{OwnerID: "own_1", BrandID: "brnd_1", RestaurantID: "out_1"}

func TestNewMember(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		mname   string
		email   string
		phone   string
		role    commonv1.Role
		wantErr error
	}{
		{name: "valid waiter", mname: "Asha", email: "a@x.com", role: commonv1.Role_ROLE_WAITER},
		{name: "valid by phone only", mname: "Bina", phone: "+9112345", role: commonv1.Role_ROLE_KITCHEN},
		{name: "blank name", mname: "  ", email: "a@x.com", role: commonv1.Role_ROLE_WAITER, wantErr: domain.ErrInvalidName},
		{name: "no contact", mname: "Asha", role: commonv1.Role_ROLE_WAITER, wantErr: domain.ErrInvalidContact},
		{name: "owner not assignable", mname: "Asha", email: "a@x.com", role: commonv1.Role_ROLE_OWNER, wantErr: domain.ErrInvalidRole},
		{name: "customer not assignable", mname: "Asha", email: "a@x.com", role: commonv1.Role_ROLE_CUSTOMER, wantErr: domain.ErrInvalidRole},
		{name: "unspecified not assignable", mname: "Asha", email: "a@x.com", role: commonv1.Role_ROLE_UNSPECIFIED, wantErr: domain.ErrInvalidRole},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m, err := domain.NewMember("stf_1", scope, tt.mname, tt.email, tt.phone, tt.role)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("want %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected: %v", err)
			}
			if !m.Active || m.OwnerID != scope.OwnerID || m.RestaurantID != scope.RestaurantID {
				t.Fatalf("bad member: %+v", m)
			}
		})
	}
}

func TestQuotaKeyAndReservationID(t *testing.T) {
	t.Parallel()
	tests := []struct {
		role    commonv1.Role
		wantKey string
	}{
		{commonv1.Role_ROLE_WAITER, "staff.waiter"},
		{commonv1.Role_ROLE_MANAGER, "staff.manager"},
		{commonv1.Role_ROLE_KITCHEN, "staff.kitchen"},
		{commonv1.Role_ROLE_CASHIER, "staff.cashier"},
		{commonv1.Role_ROLE_BRAND_ADMIN, "staff.brand_admin"},
		{commonv1.Role_ROLE_OWNER, ""},
	}
	for _, tt := range tests {
		if got := domain.QuotaKeyFor(tt.role); got != tt.wantKey {
			t.Fatalf("QuotaKeyFor(%v)=%q want %q", tt.role, got, tt.wantKey)
		}
	}

	m, _ := domain.NewMember("stf_abc", scope, "Asha", "a@x.com", "", commonv1.Role_ROLE_WAITER)
	if got, want := m.ReservationID(commonv1.Role_ROLE_WAITER), "stf_abc:waiter"; got != want {
		t.Fatalf("ReservationID=%q want %q", got, want)
	}
	// Reservation id is deterministic per (id, role) so swaps target the right slot.
	if domain.ReservationIDFor("stf_abc", commonv1.Role_ROLE_CASHIER) != "stf_abc:cashier" {
		t.Fatalf("bad reservation id for cashier")
	}
}

func TestMemberLifecycle(t *testing.T) {
	t.Parallel()
	m, _ := domain.NewMember("stf_1", scope, "Asha", "a@x.com", "", commonv1.Role_ROLE_WAITER)

	if err := m.Activate(); !errors.Is(err, domain.ErrAlreadyActive) {
		t.Fatalf("activate on active want ErrAlreadyActive, got %v", err)
	}
	if err := m.Deactivate(); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	if m.Active {
		t.Fatalf("want inactive")
	}
	if err := m.Deactivate(); !errors.Is(err, domain.ErrAlreadyInactive) {
		t.Fatalf("double deactivate want ErrAlreadyInactive, got %v", err)
	}
	if err := m.Activate(); err != nil {
		t.Fatalf("reactivate: %v", err)
	}
	if !m.Active {
		t.Fatalf("want active")
	}
}

func TestChangeRole(t *testing.T) {
	t.Parallel()
	m, _ := domain.NewMember("stf_1", scope, "Asha", "a@x.com", "", commonv1.Role_ROLE_WAITER)

	old, err := m.ChangeRole(commonv1.Role_ROLE_CASHIER)
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if old != commonv1.Role_ROLE_WAITER || m.Role != commonv1.Role_ROLE_CASHIER {
		t.Fatalf("bad swap: old=%v new=%v", old, m.Role)
	}

	if _, err := m.ChangeRole(commonv1.Role_ROLE_CASHIER); !errors.Is(err, domain.ErrSameRole) {
		t.Fatalf("same role want ErrSameRole, got %v", err)
	}
	if _, err := m.ChangeRole(commonv1.Role_ROLE_OWNER); !errors.Is(err, domain.ErrInvalidRole) {
		t.Fatalf("owner want ErrInvalidRole, got %v", err)
	}
}

func TestNewInvite(t *testing.T) {
	t.Parallel()
	m, _ := domain.NewMember("stf_1", scope, "Asha", "a@x.com", "", commonv1.Role_ROLE_WAITER)
	inv, err := domain.NewInvite("inv_1", m)
	if err != nil {
		t.Fatalf("invite: %v", err)
	}
	if inv.StaffID != m.ID || inv.OwnerID != m.OwnerID || inv.Email != m.Email {
		t.Fatalf("bad invite: %+v", inv)
	}

	m.UserID = "usr_linked"
	if _, err := domain.NewInvite("inv_2", m); !errors.Is(err, domain.ErrAlreadyLinked) {
		t.Fatalf("linked member want ErrAlreadyLinked, got %v", err)
	}
}
