package bff

import (
	"net/http"

	"connectrpc.com/connect"

	staffv1 "github.com/restorna/platform/gen/go/restorna/staff/v1"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// --- /api/manager/* -> staff + settings (role: manager / owner) ---
// Staff mutations forward the caller's token; the staff service reserves quota in
// entitlements and resolves the trusted owner/brand from the scope.

// ManagerAddStaff POST /api/manager/staff -> StaffService.AddStaff.
func (b *BFF) ManagerAddStaff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RestaurantID string `json:"restaurant_id"`
		Name         string `json:"name"`
		Email        string `json:"email"`
		Phone        string `json:"phone"`
		Role         string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Staff.AddStaff(ctx, connect.NewRequest(&staffv1.AddStaffRequest{
		RestaurantId: in.RestaurantID,
		Name:         in.Name,
		Email:        in.Email,
		Phone:        in.Phone,
		Role:         parseRole(in.Role),
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"member": staffJSON(resp.Msg.GetMember())})
}

// ManagerListStaff GET /api/manager/staff?restaurant_id= -> StaffService.ListStaff.
func (b *BFF) ManagerListStaff(w http.ResponseWriter, r *http.Request) {
	restaurantID := r.URL.Query().Get("restaurant_id")
	if restaurantID == "" {
		badRequest(w, "restaurant_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Staff.ListStaff(ctx, connect.NewRequest(&staffv1.ListStaffRequest{RestaurantId: restaurantID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	members := make([]map[string]any, 0, len(resp.Msg.GetMembers()))
	for _, m := range resp.Msg.GetMembers() {
		members = append(members, staffJSON(m))
	}
	writeJSON(w, map[string]any{"members": members})
}

// ManagerDisableStaff POST /api/manager/staff/disable -> StaffService.SetStaffActive(active=false).
func (b *BFF) ManagerDisableStaff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StaffID string `json:"staff_id"`
		Active  *bool  `json:"active"` // optional; defaults to false (disable)
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if in.StaffID == "" {
		badRequest(w, "staff_id required")
		return
	}
	active := false
	if in.Active != nil {
		active = *in.Active
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Staff.SetStaffActive(ctx, connect.NewRequest(&staffv1.SetStaffActiveRequest{
		StaffId: in.StaffID,
		Active:  active,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"member": staffJSON(resp.Msg.GetMember())})
}

// ManagerChangeRole POST /api/manager/staff/change-role -> StaffService.ChangeRole.
func (b *BFF) ManagerChangeRole(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StaffID string `json:"staff_id"`
		Role    string `json:"role"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if in.StaffID == "" {
		badRequest(w, "staff_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Staff.ChangeRole(ctx, connect.NewRequest(&staffv1.ChangeRoleRequest{
		StaffId: in.StaffID,
		Role:    parseRole(in.Role),
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"member": staffJSON(resp.Msg.GetMember())})
}

// ManagerInviteStaff POST /api/manager/staff/invite -> StaffService.InviteStaff.
func (b *BFF) ManagerInviteStaff(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StaffID string `json:"staff_id"`
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if in.StaffID == "" {
		badRequest(w, "staff_id required")
		return
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Staff.InviteStaff(ctx, connect.NewRequest(&staffv1.InviteStaffRequest{StaffId: in.StaffID}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"invite_id": resp.Msg.GetInviteId()})
}

// ManagerGetSettings GET /api/manager/settings -> SettingsService.GetEffective (scope from token).
func (b *BFF) ManagerGetSettings(w http.ResponseWriter, r *http.Request) {
	b.getSettings(w, r)
}

// ManagerSetSetting POST /api/manager/settings -> SettingsService.SetOverride (scope from token).
func (b *BFF) ManagerSetSetting(w http.ResponseWriter, r *http.Request) {
	b.setSetting(w, r)
}

func staffJSON(m *staffv1.StaffMember) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{
		"id":            m.GetId(),
		"owner_id":      m.GetOwnerId(),
		"brand_id":      m.GetBrandId(),
		"restaurant_id": m.GetRestaurantId(),
		"name":          m.GetName(),
		"email":         m.GetEmail(),
		"phone":         m.GetPhone(),
		"role":          m.GetRole().String(),
		"active":        m.GetActive(),
		"user_id":       m.GetUserId(),
	}
}
