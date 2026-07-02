package clients

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	staffv1 "github.com/restorna/platform/gen/go/restorna/staff/v1"
	"github.com/restorna/platform/gen/go/restorna/staff/v1/staffv1connect"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Staff wraps the generated StaffService client.
type Staff struct {
	rpc staffv1connect.StaffServiceClient
}

var _ ports.Staff = (*Staff)(nil)

// NewStaff dials the staff service at baseURL using the given (h2c) client.
func NewStaff(httpClient connect.HTTPClient, baseURL string) *Staff {
	return &Staff{rpc: staffv1connect.NewStaffServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewStaffFromRPC wraps an existing generated client (tests / custom wiring).
func NewStaffFromRPC(rpc staffv1connect.StaffServiceClient) *Staff {
	return &Staff{rpc: rpc}
}

// AddStaff creates a roster member. A ResourceExhausted reply (plan staff.<role>
// limit reached) is translated to ports.ErrQuotaExhausted so the saga reports it
// per-invite instead of failing the whole step.
func (c *Staff) AddStaff(ctx context.Context, restaurantID, name, email, phone, role string) (string, error) {
	res, err := c.rpc.AddStaff(ctx, connect.NewRequest(&staffv1.AddStaffRequest{
		RestaurantId: restaurantID,
		Name:         name,
		Email:        email,
		Phone:        phone,
		Role:         roleFromString(role),
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			return "", fmt.Errorf("%w: %v", ports.ErrQuotaExhausted, err)
		}
		return "", fmt.Errorf("staff.AddStaff: %w", err)
	}
	return res.Msg.GetMember().GetId(), nil
}

// InviteStaff sends the invite for an existing member.
func (c *Staff) InviteStaff(ctx context.Context, staffID string) (string, error) {
	res, err := c.rpc.InviteStaff(ctx, connect.NewRequest(&staffv1.InviteStaffRequest{
		StaffId: staffID,
	}))
	if err != nil {
		return "", fmt.Errorf("staff.InviteStaff: %w", err)
	}
	return res.Msg.GetInviteId(), nil
}

// roleFromString maps an invite role token ("manager", "waiter", ...) to the
// common Role enum. Unknown tokens map to ROLE_UNSPECIFIED, which the staff
// service rejects as an invalid role.
func roleFromString(role string) commonv1.Role {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "manager":
		return commonv1.Role_ROLE_MANAGER
	case "waiter":
		return commonv1.Role_ROLE_WAITER
	case "kitchen":
		return commonv1.Role_ROLE_KITCHEN
	case "cashier", "billing":
		return commonv1.Role_ROLE_CASHIER
	case "brand_admin", "brand-admin":
		return commonv1.Role_ROLE_BRAND_ADMIN
	default:
		return commonv1.Role_ROLE_UNSPECIFIED
	}
}
