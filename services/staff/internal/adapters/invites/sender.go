// Package invites provides InviteSender implementations. In production the
// invite is delivered by the notifications service, reached either by emitting an
// event (restorna.staff.invited.v1, staged in the outbox by the app) or by a
// direct gRPC call. This adapter is the direct-delivery port; the default
// implementation logs and relies on the staged event for fan-out.
package invites

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/restorna/platform/services/staff/internal/domain"
	"github.com/restorna/platform/services/staff/internal/ports"
)

// LogSender records that an invite would be sent. The authoritative delivery is
// the restorna.staff.invited.v1 event consumed by notifications; this keeps the
// port satisfied without coupling staff to the notifications transport.
type LogSender struct {
	log zerolog.Logger
}

var _ ports.InviteSender = (*LogSender)(nil)

// NewLogSender builds a logging InviteSender.
func NewLogSender(log zerolog.Logger) *LogSender {
	return &LogSender{log: log}
}

// Send logs the invite. The staged invited event drives actual notification.
func (s *LogSender) Send(ctx context.Context, inv domain.Invite) error {
	s.log.Info().
		Str("invite_id", inv.ID).
		Str("staff_id", inv.StaffID).
		Str("owner_id", inv.OwnerID).
		Str("email", inv.Email).
		Str("phone", inv.Phone).
		Msg("staff invite created")
	return nil
}
