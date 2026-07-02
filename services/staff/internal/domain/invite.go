package domain

import (
	"errors"
	"time"
)

// ErrAlreadyLinked is returned when an invite cannot be created because the
// member already accepted and is linked to an identity user.
var ErrAlreadyLinked = errors.New("staff member already linked to a user")

// Invite is a pending invitation for a roster member to claim their account.
// They receive it via notifications, OTP in, and the identity user is then
// linked to the member.
type Invite struct {
	ID        string
	StaffID   string
	OwnerID   string
	Email     string
	Phone     string
	CreatedAt time.Time
}

// NewInvite builds an invite for an existing member. A member already linked to
// an identity user cannot be re-invited.
func NewInvite(id string, m Member) (Invite, error) {
	if m.UserID != "" {
		return Invite{}, ErrAlreadyLinked
	}
	return Invite{
		ID:        id,
		StaffID:   m.ID,
		OwnerID:   m.OwnerID,
		Email:     m.Email,
		Phone:     m.Phone,
		CreatedAt: time.Now().UTC(),
	}, nil
}
