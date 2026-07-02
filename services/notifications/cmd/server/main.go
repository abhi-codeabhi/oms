// Command server is the composition root for the notifications service. It wires
// adapters to the app and starts the Connect server (CONVENTIONS.md main.go
// skeleton), plus the outbox relay and the delivery-status webhook consumer.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/notifications/v1/notificationsv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	hubclient "github.com/restorna/platform/services/notifications/internal/adapters/connectorhub"
	grpcadapter "github.com/restorna/platform/services/notifications/internal/adapters/grpc"
	natsadapter "github.com/restorna/platform/services/notifications/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/notifications/internal/adapters/pg"
	"github.com/restorna/platform/services/notifications/internal/adapters/providers"
	"github.com/restorna/platform/services/notifications/internal/app"
	"github.com/restorna/platform/services/notifications/migrations"
)

// Config extends the shared base with notifications-specific knobs.
type Config struct {
	config.Base
	ConnectorHubURL string `env:"CONNECTORHUB_URL" default:"http://connectorhub:8080"`
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
	hub := hubclient.New(http.DefaultClient, cfg.ConnectorHubURL)
	provider := providers.New()

	uc := app.New(repo, hub, provider, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := notificationsv1connect.NewNotificationsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Outbox relay: drains staged message.sent/failed/updated events to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	// Delivery-status consumer: applies provider delivery webhooks (via connector-hub
	// events) to messages, advancing DeliveryStatus.
	consumer := natsadapter.NewConsumer(uc, cfg.NatsURL)
	go func() {
		if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("delivery-status consumer stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("notifications service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
