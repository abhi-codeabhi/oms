// Package app holds the staff use cases. It depends only on ports + domain, and
// is where quota enforcement is orchestrated: reserve in entitlements BEFORE
// persisting, release on deactivate, swap reservations on role change.
package app

import (
	"context"
	"errors"
	"fmt"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/staff/internal/domain"
	"github.com/restorna/platform/services/staff/internal/ports"
)

// Event types emitted by the staff context (CloudEvents `type`).
const (
	EventMemberAdded       = "restorna.staff.member.added.v1"
	EventMemberDeactivated = "restorna.staff.member.deactivated.v1"
	EventMemberReactivated = "restorna.staff.member.reactivated.v1"
	EventRoleChanged       = "restorna.staff.member.role_changed.v1"
	EventInvited           = "restorna.staff.invited.v1"
)

// ErrQuotaExceeded signals the plan's staff.<role> limit is reached. It carries
// the upgrade hint so the grpc adapter can surface it on a ResourceExhausted.
type ErrQuotaExceeded struct {
	Key         string
	UpgradeHint string
}

func (e ErrQuotaExceeded) Error() string {
	return fmt.Sprintf("quota exceeded for %s", e.Key)
}

// App is the staff service use case set.
type App struct {
	repo    ports.Repo
	ent     ports.Entitlements
	invites ports.InviteSender
}

// New wires the use cases against their ports.
func New(repo ports.Repo, ent ports.Entitlements, invites ports.InviteSender) *App {
	return &App{repo: repo, ent: ent, invites: invites}
}

// memberAddedData is the payload for the member.added / lifecycle events.
type memberEventData struct {
	StaffID      string `json:"staff_id"`
	OwnerID      string `json:"owner_id"`
	BrandID      string `json:"brand_id"`
	RestaurantID string `json:"restaurant_id"`
	Name         string `json:"name"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Role         string `json:"role"`
	PreviousRole string `json:"previous_role,omitempty"`
}

func eventData(m domain.Member) memberEventData {
	return memberEventData{
		StaffID:      m.ID,
		OwnerID:      m.OwnerID,
		BrandID:      m.BrandID,
		RestaurantID: m.RestaurantID,
		Name:         m.Name,
		Email:        m.Email,
		Phone:        m.Phone,
		Role:         domain.RoleSlug(m.Role),
	}
}

// AddStaff reserves the staff.<role> quota in entitlements BEFORE persisting. If
// the reservation is rejected it returns ErrQuotaExceeded with the upgrade hint
// and persists nothing. On a persistence failure the reservation is released so
// quota is not leaked.
func (a *App) AddStaff(ctx context.Context, restaurantID, name, email, phone string, role commonv1.Role) (domain.Member, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return domain.Member{}, fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	if err := scope.Require(commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN, commonv1.Role_ROLE_MANAGER); err != nil {
		return domain.Member{}, err
	}

	rid := restaurantID
	if rid == "" {
		rid = scope.RestaurantID
	}

	m, err := domain.NewMember(ids.New("stf"), domain.Scope{
		OwnerID:      scope.OwnerID,
		BrandID:      scope.BrandID,
		RestaurantID: rid,
	}, name, email, phone, role)
	if err != nil {
		return domain.Member{}, err
	}

	key := m.QuotaKey()
	resID := m.ReservationID(m.Role)

	ok2, hint, err := a.ent.Reserve(ctx, m.OwnerID, key, 1, resID)
	if err != nil {
		return domain.Member{}, fmt.Errorf("reserve quota: %w", err)
	}
	if !ok2 {
		return domain.Member{}, ErrQuotaExceeded{Key: key, UpgradeHint: hint}
	}

	evt := &ports.OutboxEvent{Type: EventMemberAdded, Data: eventData(m)}
	if err := a.repo.Create(ctx, scope.OwnerID, m, evt); err != nil {
		// Compensate: give the reserved slot back so over-counting can't happen.
		_ = a.ent.Release(ctx, m.OwnerID, key, 1, resID)
		return domain.Member{}, fmt.Errorf("persist member: %w", err)
	}
	return m, nil
}

// ListStaff returns the roster for an outlet, paged. page_token is a numeric
// offset (kept simple; ULIDs already sort the rows deterministically).
func (a *App) ListStaff(ctx context.Context, restaurantID string, page *commonv1.PageRequest) ([]domain.Member, *commonv1.PageResponse, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	if err := scope.Require(); err != nil {
		return nil, nil, err
	}

	rid := restaurantID
	if rid == "" {
		rid = scope.RestaurantID
	}

	limit, offset := decodePage(page)
	members, total, err := a.repo.ListByRestaurant(ctx, scope.OwnerID, rid, limit, offset)
	if err != nil {
		return nil, nil, fmt.Errorf("list staff: %w", err)
	}

	next := ""
	if offset+len(members) < total {
		next = encodeToken(offset + len(members))
	}
	return members, &commonv1.PageResponse{NextPageToken: next, Total: int32(total)}, nil
}

// SetStaffActive toggles a member's active flag. Deactivating RELEASES the
// staff.<role> quota; reactivating re-RESERVES it (and fails with quota exceeded
// if the plan no longer has room). The release/reserve happens first so the
// reservation count stays consistent with the persisted state.
func (a *App) SetStaffActive(ctx context.Context, staffID string, active bool) (domain.Member, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return domain.Member{}, fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	if err := scope.Require(commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN, commonv1.Role_ROLE_MANAGER); err != nil {
		return domain.Member{}, err
	}

	m, err := a.repo.Get(ctx, scope.OwnerID, staffID)
	if err != nil {
		return domain.Member{}, err
	}

	key := m.QuotaKey()
	resID := m.ReservationID(m.Role)

	if active {
		if err := m.Activate(); err != nil {
			if errors.Is(err, domain.ErrAlreadyActive) {
				return m, nil // idempotent
			}
			return domain.Member{}, err
		}
		ok2, hint, err := a.ent.Reserve(ctx, m.OwnerID, key, 1, resID)
		if err != nil {
			return domain.Member{}, fmt.Errorf("reserve quota: %w", err)
		}
		if !ok2 {
			return domain.Member{}, ErrQuotaExceeded{Key: key, UpgradeHint: hint}
		}
		evt := &ports.OutboxEvent{Type: EventMemberReactivated, Data: eventData(m)}
		if err := a.repo.Update(ctx, scope.OwnerID, m, evt); err != nil {
			_ = a.ent.Release(ctx, m.OwnerID, key, 1, resID)
			return domain.Member{}, fmt.Errorf("persist member: %w", err)
		}
		return m, nil
	}

	// Deactivate path.
	if err := m.Deactivate(); err != nil {
		if errors.Is(err, domain.ErrAlreadyInactive) {
			return m, nil // idempotent
		}
		return domain.Member{}, err
	}
	evt := &ports.OutboxEvent{Type: EventMemberDeactivated, Data: eventData(m)}
	if err := a.repo.Update(ctx, scope.OwnerID, m, evt); err != nil {
		return domain.Member{}, fmt.Errorf("persist member: %w", err)
	}
	// Release AFTER the state is persisted so a crash leaves the slot reserved
	// (safe over-count) rather than double-freeing it.
	if err := a.ent.Release(ctx, m.OwnerID, key, 1, resID); err != nil {
		return domain.Member{}, fmt.Errorf("release quota: %w", err)
	}
	return m, nil
}

// ChangeRole reserves the NEW role's quota and releases the OLD role's. The new
// reservation is taken first; if it is rejected nothing changes. Reservation ids
// are derived from staff id + role so the swap is idempotent and the release
// targets exactly the previously-held slot.
func (a *App) ChangeRole(ctx context.Context, staffID string, role commonv1.Role) (domain.Member, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return domain.Member{}, fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	if err := scope.Require(commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN, commonv1.Role_ROLE_MANAGER); err != nil {
		return domain.Member{}, err
	}

	m, err := a.repo.Get(ctx, scope.OwnerID, staffID)
	if err != nil {
		return domain.Member{}, err
	}

	oldRole, err := m.ChangeRole(role)
	if err != nil {
		if errors.Is(err, domain.ErrSameRole) {
			return m, nil // idempotent no-op
		}
		return domain.Member{}, err
	}

	newKey := domain.QuotaKeyFor(role)
	newResID := domain.ReservationIDFor(m.ID, role)
	oldKey := domain.QuotaKeyFor(oldRole)
	oldResID := domain.ReservationIDFor(m.ID, oldRole)

	// Only reserve/release for an active member — an inactive member holds no
	// slot, so its role can be swapped without touching quota.
	mustReserve := m.Active

	if mustReserve {
		ok2, hint, err := a.ent.Reserve(ctx, m.OwnerID, newKey, 1, newResID)
		if err != nil {
			return domain.Member{}, fmt.Errorf("reserve new role quota: %w", err)
		}
		if !ok2 {
			return domain.Member{}, ErrQuotaExceeded{Key: newKey, UpgradeHint: hint}
		}
	}

	data := eventData(m)
	data.PreviousRole = domain.RoleSlug(oldRole)
	evt := &ports.OutboxEvent{Type: EventRoleChanged, Data: data}
	if err := a.repo.Update(ctx, scope.OwnerID, m, evt); err != nil {
		if mustReserve {
			_ = a.ent.Release(ctx, m.OwnerID, newKey, 1, newResID) // compensate
		}
		return domain.Member{}, fmt.Errorf("persist member: %w", err)
	}

	if mustReserve {
		// Release the old slot AFTER the new role is persisted.
		if err := a.ent.Release(ctx, m.OwnerID, oldKey, 1, oldResID); err != nil {
			return domain.Member{}, fmt.Errorf("release old role quota: %w", err)
		}
	}
	return m, nil
}

// InviteStaff creates an invite for an existing member, sends it via the
// InviteSender port, and emits restorna.staff.invited.v1 for notifications. It
// does not touch quota — the slot was reserved at AddStaff time.
func (a *App) InviteStaff(ctx context.Context, staffID string) (string, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return "", fmt.Errorf("%w: missing tenancy scope", domain.ErrNotInScope)
	}
	if err := scope.Require(commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_BRAND_ADMIN, commonv1.Role_ROLE_MANAGER); err != nil {
		return "", err
	}

	m, err := a.repo.Get(ctx, scope.OwnerID, staffID)
	if err != nil {
		return "", err
	}

	inv, err := domain.NewInvite(ids.New("inv"), m)
	if err != nil {
		return "", err
	}

	if err := a.invites.Send(ctx, inv); err != nil {
		return "", fmt.Errorf("send invite: %w", err)
	}

	// Emit the invited event (idempotent persistence of the outbox row keyed by
	// the member; the relay publishes for notifications). We stage it via an
	// Update with no member change other than touching the row's updated_at.
	evt := &ports.OutboxEvent{Type: EventInvited, Data: inviteEventData{
		InviteID:     inv.ID,
		StaffID:      m.ID,
		OwnerID:      m.OwnerID,
		RestaurantID: m.RestaurantID,
		Email:        m.Email,
		Phone:        m.Phone,
		Role:         domain.RoleSlug(m.Role),
	}}
	if err := a.repo.Update(ctx, scope.OwnerID, m, evt); err != nil {
		return "", fmt.Errorf("stage invited event: %w", err)
	}
	return inv.ID, nil
}

type inviteEventData struct {
	InviteID     string `json:"invite_id"`
	StaffID      string `json:"staff_id"`
	OwnerID      string `json:"owner_id"`
	RestaurantID string `json:"restaurant_id"`
	Email        string `json:"email,omitempty"`
	Phone        string `json:"phone,omitempty"`
	Role         string `json:"role"`
}

const defaultPageSize = 50

func decodePage(p *commonv1.PageRequest) (limit, offset int) {
	limit = defaultPageSize
	if p != nil && p.GetPageSize() > 0 {
		limit = int(p.GetPageSize())
	}
	if p != nil {
		offset = decodeToken(p.GetPageToken())
	}
	return limit, offset
}
