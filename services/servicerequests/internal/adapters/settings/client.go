// Package settings adapts the generated SettingsService Connect client to the
// app's ports.SettingsResolver interface. This is the only place that knows about
// the generated settings client; the app stays infra-free. It reads
// floor.call.cooldown_secs (rate-limit window) and floor.call.escalate_secs
// (escalation threshold) via SettingsService.GetEffective and parses them into
// durations, degrading to the package defaults (60s / 30s) when settings is
// unavailable or a value is missing/malformed.
package settings

import (
	"context"
	"strconv"
	"time"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/services/servicerequests/internal/ports"
)

// Setting keys read from SettingsService. The cooldown key already exists in the
// settings seed (floor.call.cooldown_secs); the escalate key lives in the same
// floor.call namespace.
const (
	KeyCooldownSecs = "floor.call.cooldown_secs"
	KeyEscalateSecs = "floor.call.escalate_secs"
	namespace       = "floor"
)

// Client implements ports.SettingsResolver over a Connect SettingsService client.
type Client struct {
	rpc settingsv1connect.SettingsServiceClient
}

var _ ports.SettingsResolver = (*Client)(nil)

// New builds a Client talking to the settings service at baseURL using the given
// (h2c/gRPC) http client. baseURL e.g. "http://settings:8080".
func New(httpClient connect.HTTPClient, baseURL string) *Client {
	return &Client{rpc: settingsv1connect.NewSettingsServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewFromRPC wraps an already-built generated client (tests / custom wiring).
func NewFromRPC(rpc settingsv1connect.SettingsServiceClient) *Client {
	return &Client{rpc: rpc}
}

// Thresholds resolves the cooldown + escalation windows for the restaurant. It
// asks SettingsService for both keys at the restaurant scope; any key it cannot
// resolve (settings down, missing, malformed) falls back to the package default,
// so a settings outage degrades to 60s cooldown / 30s escalation rather than
// failing the request.
func (c *Client) Thresholds(ctx context.Context, restaurantID string) (ports.Thresholds, error) {
	out := ports.Thresholds{Cooldown: ports.DefaultCooldown, Escalation: ports.DefaultEscalation}

	resp, err := c.rpc.GetEffective(ctx, connect.NewRequest(&settingsv1.GetEffectiveRequest{
		Scope:     &commonv1.TenantRef{RestaurantId: restaurantID},
		Keys:      []string{KeyCooldownSecs, KeyEscalateSecs},
		Namespace: namespace,
	}))
	if err != nil {
		// Degrade to defaults; the app treats this as "settings unavailable".
		return out, nil
	}

	for _, v := range resp.Msg.GetValues() {
		secs, ok := parseSecs(v.GetValue().GetRaw())
		if !ok {
			continue
		}
		switch v.GetKey() {
		case KeyCooldownSecs:
			out.Cooldown = secs
		case KeyEscalateSecs:
			out.Escalation = secs
		}
	}
	return out, nil
}

// parseSecs parses a settings raw value (canonical string) as a non-negative
// seconds count into a duration. Returns ok=false for empty / non-numeric /
// negative values so the caller keeps its default.
func parseSecs(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}
