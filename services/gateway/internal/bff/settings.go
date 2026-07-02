package bff

import (
	"net/http"
	"strings"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/gateway/internal/clients"
)

// getSettings resolves effective settings for the caller's tenant scope (taken from
// the verified token, never the body). Shared by /api/owner/settings and
// /api/manager/settings. Query: ?namespace=billing&keys=billing.gst_pct,billing.currency
func (b *BFF) getSettings(w http.ResponseWriter, r *http.Request) {
	scope, _ := tenancy.From(r.Context())
	namespace := r.URL.Query().Get("namespace")
	var keys []string
	if raw := r.URL.Query().Get("keys"); raw != "" {
		keys = strings.Split(raw, ",")
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Settings.GetEffective(ctx, connect.NewRequest(&settingsv1.GetEffectiveRequest{
		Scope:     scopeRef(scope),
		Keys:      keys,
		Namespace: namespace,
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, map[string]any{"values": settingsValuesJSON(resp.Msg.GetValues())})
}

// setSetting writes a single override at the caller's tenant scope. The backend
// enforces editable_by/max_scope. Shared by owner + manager surfaces.
func (b *BFF) setSetting(w http.ResponseWriter, r *http.Request) {
	scope, _ := tenancy.From(r.Context())
	var in struct {
		Key   string `json:"key"`
		Type  string `json:"type"`
		Raw   string `json:"raw"`
		Value string `json:"value"` // alias for raw
	}
	if err := decodeJSON(r, &in); err != nil {
		badRequest(w, "invalid json")
		return
	}
	if in.Key == "" {
		badRequest(w, "key required")
		return
	}
	raw := in.Raw
	if raw == "" {
		raw = in.Value
	}
	ctx := clients.WithToken(r.Context(), fwd(r))
	resp, err := b.clients.Settings.SetOverride(ctx, connect.NewRequest(&settingsv1.SetOverrideRequest{
		Scope: scopeRef(scope),
		Key:   in.Key,
		Value: &settingsv1.Value{Type: parseValueType(in.Type), Raw: raw},
	}))
	if err != nil {
		writeErr(w, err)
		return
	}
	sv := resp.Msg.GetValue()
	out := map[string]any{"key": sv.GetKey(), "source_scope": sv.GetSourceScope().String()}
	if v := sv.GetValue(); v != nil {
		out["type"] = v.GetType().String()
		out["raw"] = v.GetRaw()
	}
	writeJSON(w, out)
}

// scopeRef builds the common TenantRef from the verified tenancy scope.
func scopeRef(s tenancy.Scope) *commonv1.TenantRef {
	return &commonv1.TenantRef{
		OwnerId:      s.OwnerID,
		BrandId:      s.BrandID,
		RestaurantId: s.RestaurantID,
	}
}

func parseValueType(s string) settingsv1.ValueType {
	switch strings.ToLower(s) {
	case "int":
		return settingsv1.ValueType_INT
	case "bool":
		return settingsv1.ValueType_BOOL
	case "string":
		return settingsv1.ValueType_STRING
	case "decimal":
		return settingsv1.ValueType_DECIMAL
	case "json":
		return settingsv1.ValueType_JSON
	case "enum":
		return settingsv1.ValueType_ENUM
	default:
		return settingsv1.ValueType_VALUE_TYPE_UNSPECIFIED
	}
}
