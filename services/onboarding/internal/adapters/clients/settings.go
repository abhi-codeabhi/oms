package clients

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	settingsv1 "github.com/restorna/platform/gen/go/restorna/settings/v1"
	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

// Settings wraps the generated SettingsService client.
type Settings struct {
	rpc settingsv1connect.SettingsServiceClient
}

var _ ports.Settings = (*Settings)(nil)

// NewSettings dials the settings service at baseURL using the given (h2c) client.
func NewSettings(httpClient connect.HTTPClient, baseURL string) *Settings {
	return &Settings{rpc: settingsv1connect.NewSettingsServiceClient(httpClient, baseURL, connect.WithGRPC())}
}

// NewSettingsFromRPC wraps an existing generated client (tests / custom wiring).
func NewSettingsFromRPC(rpc settingsv1connect.SettingsServiceClient) *Settings {
	return &Settings{rpc: rpc}
}

// SetOverride writes one setting override at restaurant scope. SetOverride is an
// idempotent upsert, so re-seeding the same default is safe on a saga retry.
func (c *Settings) SetOverride(ctx context.Context, ownerID, brandID, restaurantID, key, valueType, raw string) error {
	_, err := c.rpc.SetOverride(ctx, connect.NewRequest(&settingsv1.SetOverrideRequest{
		Scope: &commonv1.TenantRef{
			OwnerId:      ownerID,
			BrandId:      brandID,
			RestaurantId: restaurantID,
		},
		Key: key,
		Value: &settingsv1.Value{
			Type: valueTypeFromString(valueType),
			Raw:  raw,
		},
	}))
	if err != nil {
		return fmt.Errorf("settings.SetOverride: %w", err)
	}
	return nil
}

// valueTypeFromString maps the app's value-type token to the settings enum.
func valueTypeFromString(t string) settingsv1.ValueType {
	switch t {
	case "INT":
		return settingsv1.ValueType_INT
	case "BOOL":
		return settingsv1.ValueType_BOOL
	case "STRING":
		return settingsv1.ValueType_STRING
	case "DECIMAL":
		return settingsv1.ValueType_DECIMAL
	case "JSON":
		return settingsv1.ValueType_JSON
	case "ENUM":
		return settingsv1.ValueType_ENUM
	default:
		return settingsv1.ValueType_VALUE_TYPE_UNSPECIFIED
	}
}
