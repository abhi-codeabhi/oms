package pg

import commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"

// roleFromInt converts the stored int32 enum value back to a common Role.
func roleFromInt(v int32) commonv1.Role {
	return commonv1.Role(v)
}
