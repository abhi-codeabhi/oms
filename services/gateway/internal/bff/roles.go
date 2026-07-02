package bff

import commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"

// parseRole maps the snake-case role strings the consoles send to the proto enum.
func parseRole(s string) commonv1.Role {
	switch s {
	case "platform_admin":
		return commonv1.Role_ROLE_PLATFORM_ADMIN
	case "owner":
		return commonv1.Role_ROLE_OWNER
	case "brand_admin":
		return commonv1.Role_ROLE_BRAND_ADMIN
	case "manager":
		return commonv1.Role_ROLE_MANAGER
	case "waiter":
		return commonv1.Role_ROLE_WAITER
	case "kitchen":
		return commonv1.Role_ROLE_KITCHEN
	case "cashier":
		return commonv1.Role_ROLE_CASHIER
	case "customer":
		return commonv1.Role_ROLE_CUSTOMER
	default:
		return commonv1.Role_ROLE_UNSPECIFIED
	}
}
