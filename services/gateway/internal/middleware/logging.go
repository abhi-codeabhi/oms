package middleware

import (
	"net/http"
	"time"

	"github.com/restorna/platform/pkg/tenancy"
	"github.com/rs/zerolog/log"
)

// Logging logs one structured line per request (method, path, status, duration,
// and the tenant/user when present), via zerolog. OTel spans are added by the
// downstream Connect client interceptors; this is the edge access log.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		ev := log.Info()
		if rec.status >= 500 {
			ev = log.Error()
		}
		if s, ok := tenancy.From(r.Context()); ok {
			ev = ev.Str("user", s.UserID).Str("owner", s.OwnerID).Str("role", s.Role.String())
		}
		ev.Str("method", r.Method).
			Str("path", r.URL.Path).
			Int("status", rec.status).
			Dur("duration", time.Since(start)).
			Msg("gateway: request")
	})
}

// statusRecorder captures the response status for logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}
