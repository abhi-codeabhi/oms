// Command server is the composition root for the floor service. It wires adapters
// to the app, starts the Connect server, the two event consumers (order.placed,
// ticket.served), and the outbox relay (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/floor/v1/floorv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	clients "github.com/restorna/platform/services/floor/internal/adapters/clients"
	grpcadapter "github.com/restorna/platform/services/floor/internal/adapters/grpc"
	natsconsumer "github.com/restorna/platform/services/floor/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/floor/internal/adapters/pg"
	"github.com/restorna/platform/services/floor/internal/app"
	"github.com/restorna/platform/services/floor/migrations"
)

// Config extends the shared base with floor-specific service endpoints.
type Config struct {
	config.Base
	KitchenURL  string `env:"KITCHEN_URL" default:"http://kitchen:8080"`
	BillingURL  string `env:"BILLING_URL" default:"http://billing:8080"`
	SettingsURL string `env:"SETTINGS_URL" default:"http://settings:8080"`
	OrderingURL string `env:"ORDERING_URL" default:"http://ordering:8080"`
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
	kitchen := clients.NewKitchen(http.DefaultClient, cfg.KitchenURL)
	billing := clients.NewBilling(http.DefaultClient, cfg.BillingURL)
	settings := clients.NewSettings(http.DefaultClient, cfg.SettingsURL)
	ordering := clients.NewOrdering(http.DefaultClient, cfg.OrderingURL)

	uc := app.New(repo, kitchen, billing, settings, ordering, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := floorv1connect.NewFloorServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Choreography: two idempotent consumers.
	consumer := natsconsumer.New(uc, cfg.NatsURL)
	go func() {
		if err := consumer.RunOrderPlaced(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("order-placed consumer stopped")
		}
	}()
	go func() {
		if err := consumer.RunTicketServed(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("ticket-served consumer stopped")
		}
	}()

	// Outbox relay: drain floor.* events to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("floor service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
