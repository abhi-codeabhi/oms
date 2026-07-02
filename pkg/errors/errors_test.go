package errors

import (
	"errors"
	"fmt"
	"testing"

	"connectrpc.com/connect"
	"github.com/restorna/platform/pkg/tenancy"
)

func TestToConnect(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want connect.Code
	}{
		{"nil", nil, 0},
		{"not found", ErrNotFound, connect.CodeNotFound},
		{"already exists", ErrAlreadyExists, connect.CodeAlreadyExists},
		{"quota", ErrQuotaExceeded, connect.CodeResourceExhausted},
		{"invalid", ErrInvalid, connect.CodeInvalidArgument},
		{"wrapped not found", fmt.Errorf("loading user: %w", ErrNotFound), connect.CodeNotFound},
		{"permission denied", tenancy.ErrPermissionDenied, connect.CodePermissionDenied},
		{"unknown -> internal", errors.New("boom"), connect.CodeInternal},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ToConnect(tc.err)
			if tc.err == nil {
				if got != nil {
					t.Fatalf("ToConnect(nil) = %v, want nil", got)
				}
				return
			}
			if connect.CodeOf(got) != tc.want {
				t.Fatalf("ToConnect code = %v, want %v", connect.CodeOf(got), tc.want)
			}
		})
	}
}

func TestField(t *testing.T) {
	err := Field(ErrInvalid, "email", "must be set")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("Field error no longer matches ErrInvalid")
	}
	if ToConnect(err).Code() != connect.CodeInvalidArgument {
		t.Fatalf("Field -> connect code = %v, want InvalidArgument", ToConnect(err).Code())
	}
	if got := err.Error(); got == "" {
		t.Fatal("Field error string empty")
	}
}

func TestFieldNilDefaultsInvalid(t *testing.T) {
	err := Field(nil, "name", "required")
	if !errors.Is(err, ErrInvalid) {
		t.Fatal("Field(nil) should default to ErrInvalid")
	}
}
