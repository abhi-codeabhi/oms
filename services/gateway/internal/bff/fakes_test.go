package bff_test

import (
	"context"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	identityv1 "github.com/restorna/platform/gen/go/restorna/identity/v1"
	"github.com/restorna/platform/gen/go/restorna/identity/v1/identityv1connect"
	staffv1 "github.com/restorna/platform/gen/go/restorna/staff/v1"
	"github.com/restorna/platform/gen/go/restorna/staff/v1/staffv1connect"
)

// fakeIdentity implements identityv1connect.IdentityServiceClient. It embeds the
// interface so it satisfies the type with zero boilerplate; only the methods a test
// exercises are overridden. Calling an un-overridden method would nil-panic, which
// is fine — tests only hit what they wire.
type fakeIdentity struct {
	identityv1connect.IdentityServiceClient

	startOtpResp *identityv1.StartOtpResponse
	startOtpErr  error
	gotStartOtp  *identityv1.StartOtpRequest
}

func (f *fakeIdentity) StartOtp(_ context.Context, req *connect.Request[identityv1.StartOtpRequest]) (*connect.Response[identityv1.StartOtpResponse], error) {
	f.gotStartOtp = req.Msg
	if f.startOtpErr != nil {
		return nil, f.startOtpErr
	}
	return connect.NewResponse(f.startOtpResp), nil
}

// fakeStaff implements staffv1connect.StaffServiceClient the same way.
type fakeStaff struct {
	staffv1connect.StaffServiceClient

	listResp *staffv1.ListStaffResponse
	listErr  error
	gotList  *staffv1.ListStaffRequest

	addResp *staffv1.AddStaffResponse
	addErr  error
	gotAdd  *staffv1.AddStaffRequest

	// lastAuth records the Authorization header seen on the most recent call so a
	// test can assert the gateway forwards the caller's token downstream.
	lastAuth string
}

func (f *fakeStaff) ListStaff(_ context.Context, req *connect.Request[staffv1.ListStaffRequest]) (*connect.Response[staffv1.ListStaffResponse], error) {
	f.gotList = req.Msg
	f.lastAuth = req.Header().Get("Authorization")
	if f.listErr != nil {
		return nil, f.listErr
	}
	return connect.NewResponse(f.listResp), nil
}

func (f *fakeStaff) AddStaff(_ context.Context, req *connect.Request[staffv1.AddStaffRequest]) (*connect.Response[staffv1.AddStaffResponse], error) {
	f.gotAdd = req.Msg
	f.lastAuth = req.Header().Get("Authorization")
	if f.addErr != nil {
		return nil, f.addErr
	}
	return connect.NewResponse(f.addResp), nil
}

// helper constructors for proto fixtures.
func staffMember(id, name string, role commonv1.Role, active bool) *staffv1.StaffMember {
	return &staffv1.StaffMember{Id: id, Name: name, Role: role, Active: active, RestaurantId: "out_1"}
}
