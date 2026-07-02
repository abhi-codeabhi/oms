// Package errors defines the shared domain sentinel errors and maps them to
// Connect codes. Domain/app code returns these typed errors; only the grpc
// adapter calls ToConnect to translate them at the edge.
package errors

import (
	stderrors "errors"

	"connectrpc.com/connect"
	"github.com/restorna/platform/pkg/tenancy"
)

// Sentinel domain errors. Services wrap these (with %w) to add context.
var (
	ErrNotFound      = stderrors.New("not found")
	ErrAlreadyExists = stderrors.New("already exists")
	ErrQuotaExceeded = stderrors.New("quota exceeded")
	ErrInvalid       = stderrors.New("invalid argument")
)

// fieldError attaches a field/message validation detail to an underlying error.
type fieldError struct {
	err   error
	field string
	msg   string
}

func (e *fieldError) Error() string { return e.field + ": " + e.msg + ": " + e.err.Error() }
func (e *fieldError) Unwrap() error { return e.err }

// Field wraps err with a validation detail (field name + message). The wrapped
// error still matches its sentinel via errors.Is.
func Field(err error, field, msg string) error {
	if err == nil {
		err = ErrInvalid
	}
	return &fieldError{err: err, field: field, msg: msg}
}

// ToConnect maps a domain error to a *connect.Error with the appropriate code.
// nil maps to nil. Unknown errors map to CodeInternal.
func ToConnect(err error) *connect.Error {
	if err == nil {
		return nil
	}
	switch {
	case stderrors.Is(err, ErrNotFound):
		return connect.NewError(connect.CodeNotFound, err)
	case stderrors.Is(err, ErrAlreadyExists):
		return connect.NewError(connect.CodeAlreadyExists, err)
	case stderrors.Is(err, ErrQuotaExceeded):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case stderrors.Is(err, ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err)
	case stderrors.Is(err, tenancy.ErrPermissionDenied):
		return connect.NewError(connect.CodePermissionDenied, err)
	}
	// Already a connect error? pass through.
	var ce *connect.Error
	if stderrors.As(err, &ce) {
		return ce
	}
	return connect.NewError(connect.CodeInternal, err)
}
