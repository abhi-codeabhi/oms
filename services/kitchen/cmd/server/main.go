// Command server is the composition root for the kitchen (KDS) service. It wires
// adapters to the app, starts the Connect server, the order-placed event consumer,
// and the outbox relay (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/kitchen/v1/kitchenv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	catalogclient "github.com/restorna/platform/services/kitchen/internal/adapters/catalog"
	grpcadapter "github.com/restorna/platform/services/kitchen/internal/adapters/grpc"
	natsconsumer "github.com/restorna/platform/services/kitchen/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/kitchen/internal/adapters/pg"
	"github.com/restorna/platform/services/kitchen/internal/app"
	"github.com/restorna/platform/services/kitchen/migrations"
)

// Config extends the shared base with kitchen-specific knobs.
type Config struct {
	config.Base
	CatalogURL string `env:"CATALOG_URL" default:"http://catalog:8080"`
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
	catalog := catalogclient.New(http.DefaultClient, cfg.CatalogURL)

	uc := app.New(repo, catalog, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := kitchenv1connect.NewKitchenServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Choreography: consume ordering.order.placed -> ReceiveTicket.
	consumer := natsconsumer.New(uc, cfg.NatsURL)
	go func() {
		if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("order-placed consumer stopped")
		}
	}()

	// Outbox relay: drain ticket.ready / ticket.served to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("kitchen service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
