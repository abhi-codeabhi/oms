package tenancy

import (
	"context"
	"errors"
	"testing"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
)

func TestWithFrom(t *testing.T) {
	want := Scope{OwnerID: "own_1", BrandID: "brnd_1", Role: commonv1.Role_ROLE_OWNER, UserID: "usr_1"}
	ctx := With(context.Background(), want)
	got, ok := From(ctx)
	if !ok {
		t.Fatal("From returned ok=false after With")
	}
	if got != want {
		t.Fatalf("From = %+v, want %+v", got, want)
	}
}

func TestFromMissing(t *testing.T) {
	if _, ok := From(context.Background()); ok {
		t.Fatal("From returned ok=true for empty context")
	}
}

func TestRequire(t *testing.T) {
	tests := []struct {
		name    string
		scope   Scope
		allowed []commonv1.Role
		wantErr bool
	}{
		{"role in set", Scope{Role: commonv1.Role_ROLE_OWNER}, []commonv1.Role{commonv1.Role_ROLE_OWNER}, false},
		{"role among many", Scope{Role: commonv1.Role_ROLE_MANAGER}, []commonv1.Role{commonv1.Role_ROLE_OWNER, commonv1.Role_ROLE_MANAGER}, false},
		{"role not in set", Scope{Role: commonv1.Role_ROLE_WAITER}, []commonv1.Role{commonv1.Role_ROLE_OWNER}, true},
		{"no roles, has role", Scope{Role: commonv1.Role_ROLE_WAITER}, nil, false},
		{"no roles, unspecified", Scope{Role: commonv1.Role_ROLE_UNSPECIFIED}, nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.scope.Require(tc.allowed...)
			if tc.wantErr {
				if !errors.Is(err, ErrPermissionDenied) {
					t.Fatalf("Require err = %v, want ErrPermissionDenied", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Require unexpected err: %v", err)
			}
		})
	}
}
