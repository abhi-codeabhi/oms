// Command server is the composition root for the connector-hub service. It wires
// adapters to the app and starts the Connect server (CONVENTIONS.md main.go
// skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/connector/v1/connectorv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	cryptoadapter "github.com/restorna/platform/services/connectorhub/internal/adapters/crypto"
	entclient "github.com/restorna/platform/services/connectorhub/internal/adapters/entitlements"
	grpcadapter "github.com/restorna/platform/services/connectorhub/internal/adapters/grpc"
	natsadapter "github.com/restorna/platform/services/connectorhub/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/connectorhub/internal/adapters/pg"
	registryadapter "github.com/restorna/platform/services/connectorhub/internal/adapters/registry"
	"github.com/restorna/platform/services/connectorhub/internal/app"
	"github.com/restorna/platform/services/connectorhub/migrations"
)

// Config extends the shared base with connector-hub-specific knobs.
type Config struct {
	config.Base
	EntitlementsURL string `env:"ENTITLEMENTS_URL" default:"http://entitlements:8080"`
	// ConnectorKEK is the Key-Encryption-Key (base64/hex/raw 32 bytes) protecting
	// secret connector config at rest. No default: the service refuses to start
	// without it rather than encrypting with a weak key.
	ConnectorKEK string `env:"CONNECTOR_KEK"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load[Config]()
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("open postgres")
	}
	defer pool.Close()
	if err := pg.Migrate(cfg.DatabaseURL, migrations.FS); err != nil {
		log.Fatal().Err(err).Msg("run migrations")
	}

	bus, err := eventbus.Connect(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("connect nats")
	}

	crypto, err := cryptoadapter.FromKEK(cfg.ConnectorKEK)
	if err != nil {
		log.Fatal().Err(err).Msg("init crypto (CONNECTOR_KEK)")
	}

	// Adapters -> ports.
	repo := pgadapter.New(pool)
	ents := entclient.New(http.DefaultClient, cfg.EntitlementsURL)
	conns := registryadapter.New()
	publisher := natsadapter.New(bus)

	uc := app.New(repo, ents, crypto, conns, publisher, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := connectorv1connect.NewConnectorHubServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("connector-hub service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
