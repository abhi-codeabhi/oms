package clients

import (
	"context"
	"fmt"
	"net/http"

	"connectrpc.com/connect"

	connectorv1 "github.com/restorna/platform/gen/go/restorna/connector/v1"
	"github.com/restorna/platform/gen/go/restorna/connector/v1/connectorv1connect"
	"github.com/restorna/platform/pkg/connector"
	"github.com/restorna/platform/pkg/connectors"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

// ConnectorHub implements ports.ConnectorHub. It uses ConnectorHubService.Resolve
// to pick the active aggregator connector for the tenant (and get its decrypted
// config), then instantiates the concrete pkg/connectors AggregatorConnector
// adapter from that config and calls its PushMenu. The aggregator adapters are
// dependency-light and talk directly to Zomato/Swiggy (or the mock), so the menu
// push does not round-trip back through the hub.
type ConnectorHub struct {
	svc     connectorv1connect.ConnectorHubServiceClient
	factory func(connectorID string) (connector.AggregatorConnector, bool)
}

var _ ports.ConnectorHub = (*ConnectorHub)(nil)

// NewConnectorHub builds a ConnectorHub client talking to connector-hub at baseURL.
func NewConnectorHub(httpClient *http.Client, baseURL string, opts ...connect.ClientOption) *ConnectorHub {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &ConnectorHub{
		svc:     connectorv1connect.NewConnectorHubServiceClient(httpClient, baseURL, opts...),
		factory: defaultAggregatorFactory,
	}
}

// NewConnectorHubFromClient wraps an already-built generated client (tests/wiring).
func NewConnectorHubFromClient(svc connectorv1connect.ConnectorHubServiceClient) *ConnectorHub {
	return &ConnectorHub{svc: svc, factory: defaultAggregatorFactory}
}

// Resolve implements ports.ConnectorHub. It asks the hub for the active
// aggregator connector for the tenant (auth-scoped), optionally preferring a
// specific connector id.
func (c *ConnectorHub) Resolve(ctx context.Context, _ string, preferConnectorID string) (ports.ResolvedConnector, error) {
	resp, err := c.svc.Resolve(ctx, connect.NewRequest(&connectorv1.ResolveRequest{
		Capability:        connectorv1.Capability_CAPABILITY_AGGREGATOR,
		PreferConnectorId: preferConnectorID,
	}))
	if err != nil {
		return ports.ResolvedConnector{}, err
	}
	m := resp.Msg
	if m.GetConnectorId() == "" {
		return ports.ResolvedConnector{}, fmt.Errorf("connector-hub: no aggregator connector installed")
	}
	return ports.ResolvedConnector{
		ConnectorID:    m.GetConnectorId(),
		InstallationID: m.GetInstallationId(),
		TestMode:       m.GetTestMode(),
		Config:         m.GetConfig(),
	}, nil
}

// PushMenu implements ports.ConnectorHub. It instantiates the resolved aggregator
// adapter from the tenant config and pushes the serialized menu to it.
func (c *ConnectorHub) PushMenu(ctx context.Context, rc ports.ResolvedConnector, menuJSON []byte) (int, error) {
	adapter, ok := c.factory(rc.ConnectorID)
	if !ok {
		return 0, fmt.Errorf("connector-hub: unknown aggregator connector %q", rc.ConnectorID)
	}
	if err := adapter.Init(ctx, rc.Config); err != nil {
		return 0, fmt.Errorf("init %s: %w", rc.ConnectorID, err)
	}
	return adapter.PushMenu(ctx, menuJSON)
}

// defaultAggregatorFactory instantiates a pkg/connectors aggregator adapter by id.
func defaultAggregatorFactory(connectorID string) (connector.AggregatorConnector, bool) {
	switch connectorID {
	case "zomato":
		return connectors.NewZomato(), true
	case "swiggy":
		return connectors.NewSwiggy(), true
	case "mockagg":
		return connectors.NewMockAgg(), true
	default:
		return nil, false
	}
}
