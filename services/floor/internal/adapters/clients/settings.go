// Package clients: SettingsService -> ports.SettingsResolver. The floor reads the
// effective nudge config (floor.nudge.* keys) for a restaurant via GetEffective
// (override -> default). Missing/invalid keys fall back to the domain defaults so
// the floor always has a working config.
package clients

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/services/floor/internal/domain"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// Setting keys the floor reads from SettingsService (dotted namespace floor.nudge).
const (
	keyGreetSecs       = "floor.nudge.greet_secs"
	keyCheckinSecs     = "floor.nudge.checkin_secs"
	keyAnythingSecs    = "floor.nudge.anything_secs"
	keyGreetEnabled    = "floor.nudge.greet_enabled"
	keyCheckinEnabled  = "floor.nudge.checkin_enabled"
	keyAnythingEnabled = "floor.nudge.anything_enabled"

	nudgeNamespace = "floor.nudge"
)

// SettingsClient implements ports.SettingsResolver over a Connect SettingsService.
type SettingsClient struct {
	svc settingsv1connect.SettingsServiceClient
}

var _ ports.SettingsResolver = (*SettingsClient)(nil)

// NewSettings builds a SettingsClient talking to the settings service at baseURL.
func NewSettings(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *SettingsClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &SettingsClient{svc: settingsv1connect.NewSettingsServiceClient(httpClient, baseURL, opts...)}
}

// NewSettingsFromClient wraps an already-built generated client (tests/wiring).
func NewSettingsFromClient(svc settingsv1connect.SettingsServiceClient) *SettingsClient {
	return &SettingsClient{svc: svc}
}

// NudgeConfig resolves floor.nudge.* for the restaurant (override -> default). It
// starts from the domain defaults and applies any effective values returned.
func (c *SettingsClient) NudgeConfig(ctx context.Context, restaurantID string) (domain.NudgeConfig, error) {
	cfg := domain.DefaultNudgeConfig()
	resp, err := c.svc.GetEffective(ctx, connect.NewRequest(&settingsv1.GetEffectiveRequest{
		Scope:     &commonv1.TenantRef{RestaurantId: restaurantID},
		Namespace: nudgeNamespace,
		Keys: []string{
			keyGreetSecs, keyCheckinSecs, keyAnythingSecs,
			keyGreetEnabled, keyCheckinEnabled, keyAnythingEnabled,
		},
	}))
	if err != nil {
		return cfg, err
	}
	for _, v := range resp.Msg.GetValues() {
		raw := ""
		if val := v.GetValue(); val != nil {
			raw = strings.TrimSpace(val.GetRaw())
		}
		switch v.GetKey() {
		case keyGreetSecs:
			if n, ok := parseNonNegInt(raw); ok {
				cfg.GreetDelaySecs = n
			}
		case keyCheckinSecs:
			if n, ok := parseNonNegInt(raw); ok {
				cfg.CheckinAfterServeSecs = n
			}
		case keyAnythingSecs:
			if n, ok := parseNonNegInt(raw); ok {
				cfg.AnythingAfterCheckinSecs = n
			}
		case keyGreetEnabled:
			cfg.GreetEnabled = parseBool(raw, cfg.GreetEnabled)
		case keyCheckinEnabled:
			cfg.CheckinEnabled = parseBool(raw, cfg.CheckinEnabled)
		case keyAnythingEnabled:
			cfg.AnythingEnabled = parseBool(raw, cfg.AnythingEnabled)
		}
	}
	return cfg, nil
}

// parseNonNegInt parses a non-negative integer; ok=false leaves the default.
func parseNonNegInt(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// parseBool parses a bool, keeping the fallback on a blank/invalid value.
func parseBool(s string, fallback bool) bool {
	if s == "" {
		return fallback
	}
	b, err := strconv.ParseBool(s)
	if err != nil {
		return fallback
	}
	return b
}
