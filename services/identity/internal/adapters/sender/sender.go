// Package sender implements the OTP delivery port. The log/no-op sender ships
// now; the real SMS/email path will go through the notifications service later.
package sender

import (
	"context"
	"log/slog"

	"github.com/restorna/platform/services/identity/internal/domain"
	"github.com/restorna/platform/services/identity/internal/ports"
)

// LogSender writes the OTP delivery intent to the structured log instead of
// actually sending it. Useful for dev/local; never logs codes in production.
type LogSender struct {
	log     *slog.Logger
	devMode bool
}

// NewLog builds a LogSender. When devMode is true the code is included in the
// log line so a developer can complete the flow locally; otherwise it's redacted.
func NewLog(log *slog.Logger, devMode bool) *LogSender {
	if log == nil {
		log = slog.Default()
	}
	return &LogSender{log: log, devMode: devMode}
}

// Send records the OTP delivery. In dev it logs the code; otherwise it logs only
// that a code was dispatched.
func (s *LogSender) Send(ctx context.Context, channel domain.Channel, address, code string) error {
	if s.devMode {
		s.log.InfoContext(ctx, "otp dispatched (dev)",
			"channel", channel.String(), "address", address, "code", code)
		return nil
	}
	s.log.InfoContext(ctx, "otp dispatched",
		"channel", channel.String(), "address", address)
	return nil
}

// compile-time assertion that LogSender satisfies the port.
var _ ports.Sender = (*LogSender)(nil)
