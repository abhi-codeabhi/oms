// Package bff exposes per-surface Backend-For-Frontend HTTP handlers that the React
// consoles call with plain JSON. Each handler maps JSON <-> a backend Connect client
// call, forwards the caller's verified token (so backends apply RLS/authZ), and maps
// Connect error codes back to HTTP status. Handlers are THIN: no business logic — the
// owning service enforces quotas, validation, and tenancy.
package bff

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect"

	"github.com/restorna/platform/services/gateway/internal/clients"
	"github.com/restorna/platform/services/gateway/internal/middleware"
)

// BFF carries the backend client set shared by every surface handler.
type BFF struct {
	clients *clients.Set
}

// New builds the BFF handler group over a client set.
func New(set *clients.Set) *BFF { return &BFF{clients: set} }

// decodeJSON reads the JSON request body into v. An empty body decodes to the zero
// value (handlers that need no input pass a throwaway struct).
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// writeJSON writes v as a 200 JSON response.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

// writeErr renders an error: Connect errors map to their HTTP status, decode errors
// to 400, everything else to 500.
func writeErr(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	status := http.StatusInternalServerError
	var cerr *connect.Error
	if errors.As(err, &cerr) {
		status = connectToHTTP(cerr.Code())
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// badRequest renders a 400 with msg.
func badRequest(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// fwd is shorthand: forward the inbound bearer token onto the downstream call ctx.
func fwd(r *http.Request) (token string) {
	t, _ := middleware.TokenFrom(r.Context())
	return t
}

// connectToHTTP maps Connect codes to HTTP status codes for the JSON BFFs.
func connectToHTTP(c connect.Code) int {
	switch c {
	case connect.CodeInvalidArgument, connect.CodeOutOfRange:
		return http.StatusBadRequest
	case connect.CodeUnauthenticated:
		return http.StatusUnauthorized
	case connect.CodePermissionDenied:
		return http.StatusForbidden
	case connect.CodeNotFound:
		return http.StatusNotFound
	case connect.CodeAlreadyExists, connect.CodeAborted:
		return http.StatusConflict
	case connect.CodeResourceExhausted:
		return http.StatusTooManyRequests
	case connect.CodeFailedPrecondition:
		return http.StatusPreconditionFailed
	case connect.CodeUnavailable:
		return http.StatusServiceUnavailable
	case connect.CodeDeadlineExceeded:
		return http.StatusGatewayTimeout
	case connect.CodeUnimplemented:
		return http.StatusNotImplemented
	default:
		return http.StatusInternalServerError
	}
}
