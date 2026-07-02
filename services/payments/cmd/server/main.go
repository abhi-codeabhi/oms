// Command server is the composition root for the payments service. It wires
// adapters to the app and starts the Connect server, the payment-webhook event
// consumer (choreography), and the outbox relay (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/payments/v1/paymentsv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	connectorhubclient "github.com/restorna/platform/services/payments/internal/adapters/connectorhub"
	grpcadapter "github.com/restorna/platform/services/payments/internal/adapters/grpc"
	natsconsumer "github.com/restorna/platform/services/payments/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/payments/internal/adapters/pg"
	"github.com/restorna/platform/services/payments/internal/adapters/providers"
	"github.com/restorna/platform/services/payments/internal/app"
	"github.com/restorna/platform/services/payments/migrations"
)

// Config extends the shared base with payments-specific knobs.
type Config struct {
	config.Base
	ConnectorHubURL string `env:"CONNECTORHUB_URL" default:"http://connector-hub:8080"`
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
	hub := connectorhubclient.New(http.DefaultClient, cfg.ConnectorHubURL)
	factory := providers.New()

	uc := app.New(repo, hub, factory, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := paymentsv1connect.NewPaymentsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Choreography: consume connector-hub payment webhooks -> flip status + emit
	// restorna.payments.captured.v1 / .failed.v1.
	consumer := natsconsumer.New(uc, cfg.NatsURL)
	go func() {
		if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("payment-webhook consumer stopped")
		}
	}()

	// Outbox relay: drain payments.captured / .failed / .refunded to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("payments service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
