package clients

import (
	"context"
	"net/http"
	"strconv"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/billing/internal/domain"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// Settings keys read from SettingsService for the billing namespace.
const (
	keyGSTPct           = "billing.gst_pct"
	keyServiceChargePct = "billing.service_charge_pct"
	keyRounding         = "billing.rounding"
	keyCurrency         = "billing.currency"
	namespaceBilling    = "billing"
)

// Settings implements ports.Settings over a Connect SettingsService client. It
// resolves the effective billing tax config (GetEffective) for a restaurant.
type Settings struct {
	svc settingsv1connect.SettingsServiceClient
}

var _ ports.Settings = (*Settings)(nil)

// NewSettings builds a Settings client talking to baseURL (e.g. "http://settings:8080").
func NewSettings(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *Settings {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &Settings{svc: settingsv1connect.NewSettingsServiceClient(httpClient, baseURL, opts...)}
}

// NewSettingsFromClient wraps an already-built generated client (tests/wiring).
func NewSettingsFromClient(svc settingsv1connect.SettingsServiceClient) *Settings {
	return &Settings{svc: svc}
}

// BillingConfig returns the effective TaxConfig for the restaurant. The scope is
// built from the caller's tenancy (owner/brand/restaurant); GetEffective resolves
// each key override -> default. Missing/blank values fall back to sane defaults
// (GST 5%, no service charge, no rounding, INR).
func (s *Settings) BillingConfig(ctx context.Context, restaurantID string) (domain.TaxConfig, error) {
	cfg := domain.TaxConfig{GSTPct: 5, ServiceChargePct: 0, Rounding: domain.RoundNone, Currency: domain.DefaultCurrency}

	scope := &commonv1.TenantRef{RestaurantId: restaurantID}
	if sc, ok := tenancy.From(ctx); ok {
		scope = &commonv1.TenantRef{OwnerId: sc.OwnerID, BrandId: sc.BrandID, RestaurantId: restaurantID}
	}

	resp, err := s.svc.GetEffective(ctx, connect.NewRequest(&settingsv1.GetEffectiveRequest{
		Scope:     scope,
		Namespace: namespaceBilling,
		Keys:      []string{keyGSTPct, keyServiceChargePct, keyRounding, keyCurrency},
	}))
	if err != nil {
		return cfg, err
	}

	for _, sv := range resp.Msg.GetValues() {
		raw := ""
		if v := sv.GetValue(); v != nil {
			raw = v.GetRaw()
		}
		switch sv.GetKey() {
		case keyGSTPct:
			if f, perr := strconv.ParseFloat(raw, 64); perr == nil {
				cfg.GSTPct = f
			}
		case keyServiceChargePct:
			if f, perr := strconv.ParseFloat(raw, 64); perr == nil {
				cfg.ServiceChargePct = f
			}
		case keyRounding:
			if raw != "" {
				cfg.Rounding = domain.ParseRounding(raw)
			}
		case keyCurrency:
			if raw != "" {
				cfg.Currency = raw
			}
		}
	}
	return cfg, nil
}
