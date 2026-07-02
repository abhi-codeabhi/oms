// Package grpc is the Connect transport adapter: it implements the generated
// SettingsServiceHandler, maps proto messages <-> domain types, and converts
// domain errors to Connect codes (via pkg/errors). It is the only layer that knows
// about connect or the generated code.
//
// Tenancy: the owner id is taken from the JWT-derived tenancy.Scope (never the
// request body); brand/restaurant targeting comes from the request's TenantRef but
// is always nested under the caller's owner.
package grpc

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	pkgerrors "github.com/restorna/platform/pkg/errors"
	"github.com/restorna/platform/pkg/tenancy"

	"github.com/restorna/platform/services/settings/internal/app"
	"github.com/restorna/platform/services/settings/internal/domain"
)

// Handler adapts the app.Service to the generated Connect handler interface.
type Handler struct {
	settingsv1connect.UnimplementedSettingsServiceHandler
	svc *app.Service
}

var _ settingsv1connect.SettingsServiceHandler = (*Handler)(nil)

// New constructs a Connect handler around the use cases.
func New(svc *app.Service) *Handler { return &Handler{svc: svc} }

// RegisterDefinitions upserts a batch of definitions (services self-register).
func (h *Handler) RegisterDefinitions(ctx context.Context, req *connect.Request[settingsv1.RegisterDefinitionsRequest]) (*connect.Response[settingsv1.RegisterDefinitionsResponse], error) {
	defs := make([]domain.Definition, 0, len(req.Msg.GetDefinitions()))
	for _, d := range req.Msg.GetDefinitions() {
		defs = append(defs, fromProtoDefinition(d))
	}
	n, err := h.svc.RegisterDefinitions(ctx, defs)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&settingsv1.RegisterDefinitionsResponse{Count: int32(n)}), nil
}

// ListDefinitions returns the definition catalog filtered by namespace.
func (h *Handler) ListDefinitions(ctx context.Context, req *connect.Request[settingsv1.ListDefinitionsRequest]) (*connect.Response[settingsv1.ListDefinitionsResponse], error) {
	defs, err := h.svc.ListDefinitions(ctx, req.Msg.GetNamespace())
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*settingsv1.Definition, 0, len(defs))
	for _, d := range defs {
		out = append(out, toProtoDefinition(d))
	}
	return connect.NewResponse(&settingsv1.ListDefinitionsResponse{Definitions: out}), nil
}

// SetOverride stores a value at the scope implied by the request's TenantRef
// (owner taken from the JWT). editable_by + max_scope + validation are enforced in
// the app layer.
func (h *Handler) SetOverride(ctx context.Context, req *connect.Request[settingsv1.SetOverrideRequest]) (*connect.Response[settingsv1.SetOverrideResponse], error) {
	owner := ownerFromCtx(ctx, req.Msg.GetScope())
	ref := req.Msg.GetScope()
	sv, err := h.svc.SetOverride(ctx, app.SetOverrideInput{
		OwnerID:      owner,
		BrandID:      ref.GetBrandId(),
		RestaurantID: ref.GetRestaurantId(),
		Key:          req.Msg.GetKey(),
		Value:        fromProtoValue(req.Msg.GetValue()),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&settingsv1.SetOverrideResponse{Value: toProtoSettingValue(sv)}), nil
}

// GetEffective resolves keys for a tenant scope (override -> default). Hot path.
func (h *Handler) GetEffective(ctx context.Context, req *connect.Request[settingsv1.GetEffectiveRequest]) (*connect.Response[settingsv1.GetEffectiveResponse], error) {
	owner := ownerFromCtx(ctx, req.Msg.GetScope())
	ref := req.Msg.GetScope()
	vals, err := h.svc.GetEffective(ctx, app.GetEffectiveInput{
		OwnerID:      owner,
		BrandID:      ref.GetBrandId(),
		RestaurantID: ref.GetRestaurantId(),
		Keys:         req.Msg.GetKeys(),
		Namespace:    req.Msg.GetNamespace(),
	})
	if err != nil {
		return nil, toConnect(err)
	}
	out := make([]*settingsv1.SettingValue, 0, len(vals))
	for _, v := range vals {
		out = append(out, toProtoSettingValue(v))
	}
	return connect.NewResponse(&settingsv1.GetEffectiveResponse{Values: out}), nil
}

// ---- tenancy --------------------------------------------------------------

// ownerFromCtx returns the JWT-derived owner id, falling back to the request's
// TenantRef owner only when no scope is in context (e.g. service-to-service
// self-registration paths). The JWT always wins when present.
func ownerFromCtx(ctx context.Context, ref *commonv1.TenantRef) string {
	if s, ok := tenancy.From(ctx); ok && s.OwnerID != "" {
		return s.OwnerID
	}
	return ref.GetOwnerId()
}

// RoleFromCtx is the app.RoleFn implementation: it reads the role from the
// tenancy.Scope placed in context by the auth interceptor.
func RoleFromCtx(ctx context.Context) (string, bool) {
	s, ok := tenancy.From(ctx)
	if !ok {
		return "", false
	}
	return roleName(s.Role), true
}

// roleName maps the proto Role enum to the lowercase token editable_by uses.
func roleName(r commonv1.Role) string {
	switch r {
	case commonv1.Role_ROLE_PLATFORM_ADMIN:
		return "platform_admin"
	case commonv1.Role_ROLE_OWNER:
		return "owner"
	case commonv1.Role_ROLE_BRAND_ADMIN:
		return "brand_admin"
	case commonv1.Role_ROLE_MANAGER:
		return "manager"
	default:
		return ""
	}
}

// ---- mapping helpers ------------------------------------------------------

func toProtoDefinition(d domain.Definition) *settingsv1.Definition {
	return &settingsv1.Definition{
		Key:          d.Key,
		Title:        d.Title,
		Description:  d.Description,
		Type:         toProtoType(d.Type),
		Default:      toProtoValue(d.Default),
		MaxScope:     toProtoScope(d.MaxScope),
		EnumOptions:  d.EnumOptions,
		Validation:   d.Validation,
		EditableBy:   d.EditableBy,
		FeatureGated: d.FeatureGated,
	}
}

func fromProtoDefinition(d *settingsv1.Definition) domain.Definition {
	return domain.Definition{
		Key:          d.GetKey(),
		Title:        d.GetTitle(),
		Description:  d.GetDescription(),
		Type:         fromProtoType(d.GetType()),
		Default:      fromProtoValue(d.GetDefault()),
		MaxScope:     fromProtoScope(d.GetMaxScope()),
		EnumOptions:  d.GetEnumOptions(),
		Validation:   d.GetValidation(),
		EditableBy:   d.GetEditableBy(),
		FeatureGated: d.GetFeatureGated(),
	}
}

func toProtoSettingValue(sv domain.SettingValue) *settingsv1.SettingValue {
	return &settingsv1.SettingValue{
		Key:         sv.Key,
		Value:       toProtoValue(sv.Value),
		SourceScope: toProtoScope(sv.SourceScope),
	}
}

func toProtoValue(v domain.Value) *settingsv1.Value {
	return &settingsv1.Value{Type: toProtoType(v.Type), Raw: v.Raw}
}

func fromProtoValue(v *settingsv1.Value) domain.Value {
	if v == nil {
		return domain.Value{}
	}
	return domain.Value{Type: fromProtoType(v.GetType()), Raw: v.GetRaw()}
}

func toProtoType(t domain.ValueType) settingsv1.ValueType {
	switch t {
	case domain.TypeInt:
		return settingsv1.ValueType_INT
	case domain.TypeBool:
		return settingsv1.ValueType_BOOL
	case domain.TypeString:
		return settingsv1.ValueType_STRING
	case domain.TypeDecimal:
		return settingsv1.ValueType_DECIMAL
	case domain.TypeJSON:
		return settingsv1.ValueType_JSON
	case domain.TypeEnum:
		return settingsv1.ValueType_ENUM
	default:
		return settingsv1.ValueType_VALUE_TYPE_UNSPECIFIED
	}
}

func fromProtoType(t settingsv1.ValueType) domain.ValueType {
	switch t {
	case settingsv1.ValueType_INT:
		return domain.TypeInt
	case settingsv1.ValueType_BOOL:
		return domain.TypeBool
	case settingsv1.ValueType_STRING:
		return domain.TypeString
	case settingsv1.ValueType_DECIMAL:
		return domain.TypeDecimal
	case settingsv1.ValueType_JSON:
		return domain.TypeJSON
	case settingsv1.ValueType_ENUM:
		return domain.TypeEnum
	default:
		return domain.TypeUnspecified
	}
}

func toProtoScope(s domain.Scope) settingsv1.Scope {
	switch s {
	case domain.ScopeOwner:
		return settingsv1.Scope_SCOPE_OWNER
	case domain.ScopeBrand:
		return settingsv1.Scope_SCOPE_BRAND
	case domain.ScopeRestaurant:
		return settingsv1.Scope_SCOPE_RESTAURANT
	default:
		// ScopeDefinition (and unspecified) have no proto member; default fall-back
		// reports SCOPE_UNSPECIFIED so callers read "came from the definition default".
		return settingsv1.Scope_SCOPE_UNSPECIFIED
	}
}

func fromProtoScope(s settingsv1.Scope) domain.Scope {
	switch s {
	case settingsv1.Scope_SCOPE_OWNER:
		return domain.ScopeOwner
	case settingsv1.Scope_SCOPE_BRAND:
		return domain.ScopeBrand
	case settingsv1.Scope_SCOPE_RESTAURANT:
		return domain.ScopeRestaurant
	default:
		return domain.ScopeUnspecified
	}
}

// toConnect normalises domain errors to the shared pkg/errors sentinels so
// pkg/errors.ToConnect can assign the right Connect code.
func toConnect(err error) error {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return pkgerrors.ToConnect(pkgerrors.ErrNotFound)
	case errors.Is(err, domain.ErrNotEditable), errors.Is(err, domain.ErrScopeTooDeep):
		return connect.NewError(connect.CodePermissionDenied, err)
	case errors.Is(err, domain.ErrInvalid):
		return pkgerrors.ToConnect(pkgerrors.ErrInvalid)
	default:
		return pkgerrors.ToConnect(err)
	}
}
