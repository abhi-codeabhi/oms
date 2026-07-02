// Command server is the composition root for the aggregators service. It wires
// adapters to the app, starts the Connect server, the aggregator-order event
// consumer, and the outbox relay (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/aggregators/v1/aggregatorsv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	clients "github.com/restorna/platform/services/aggregators/internal/adapters/clients"
	grpcadapter "github.com/restorna/platform/services/aggregators/internal/adapters/grpc"
	natsconsumer "github.com/restorna/platform/services/aggregators/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/aggregators/internal/adapters/pg"
	"github.com/restorna/platform/services/aggregators/internal/app"
	"github.com/restorna/platform/services/aggregators/migrations"
)

// Config extends the shared base with aggregators-specific service endpoints.
type Config struct {
	config.Base
	ConnectorHubURL string `env:"CONNECTORHUB_URL" default:"http://connector-hub:8080"`
	CatalogURL      string `env:"CATALOG_URL" default:"http://catalog:8080"`
	OrderingURL     string `env:"ORDERING_URL" default:"http://ordering:8080"`
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

	// Adapters -> ports.
	repo := pgadapter.New(pool)
	hub := clients.NewConnectorHub(http.DefaultClient, cfg.ConnectorHubURL)
	catalog := clients.NewCatalog(http.DefaultClient, cfg.CatalogURL)
	ordering := clients.NewOrdering(http.DefaultClient, cfg.OrderingURL)

	uc := app.New(repo, hub, catalog, ordering, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := aggregatorsv1connect.NewAggregatorsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Choreography: consume connector.aggregator.order.received -> persist + forward.
	consumer := natsconsumer.New(uc, cfg.NatsURL)
	go func() {
		if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("aggregator-order consumer stopped")
		}
	}()

	// Outbox relay: drain aggregators.order.received to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("aggregators service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
