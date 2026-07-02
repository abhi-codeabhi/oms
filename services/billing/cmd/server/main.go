// Command server is the composition root for the billing (operational table
// billing) service. It wires adapters to the app, starts the Connect server, the
// billing-board event consumers, and the outbox relay (CONVENTIONS.md main.go
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

	"github.com/restorna/platform/gen/go/restorna/billing/v1/billingv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	clients "github.com/restorna/platform/services/billing/internal/adapters/clients"
	grpcadapter "github.com/restorna/platform/services/billing/internal/adapters/grpc"
	natsconsumer "github.com/restorna/platform/services/billing/internal/adapters/nats"
	pgadapter "github.com/restorna/platform/services/billing/internal/adapters/pg"
	"github.com/restorna/platform/services/billing/internal/app"
	"github.com/restorna/platform/services/billing/migrations"
)

// Config extends the shared base with billing-specific service endpoints.
type Config struct {
	config.Base
	OrderingURL   string `env:"ORDERING_URL" default:"http://ordering:8080"`
	CatalogURL    string `env:"CATALOG_URL" default:"http://catalog:8080"`
	SettingsURL   string `env:"SETTINGS_URL" default:"http://settings:8080"`
	PromotionsURL string `env:"PROMOTIONS_URL" default:"http://promotions:8080"`
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
	orders := clients.NewOrdering(http.DefaultClient, cfg.OrderingURL)
	menu := clients.NewCatalog(http.DefaultClient, cfg.CatalogURL)
	settings := clients.NewSettings(http.DefaultClient, cfg.SettingsURL)
	promos := clients.NewPromotions(http.DefaultClient, cfg.PromotionsURL)

	uc := app.New(repo, orders, menu, settings, promos, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := billingv1connect.NewBillingServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Event-driven billing board: consume order.placed / raised(bill) /
	// bill.opened / bill.finalized to maintain the tabs read model.
	consumer := natsconsumer.New(uc, cfg.NatsURL)
	go func() {
		if err := consumer.Run(ctx); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("billing board consumer stopped")
		}
	}()

	// Outbox relay: drain bill.opened / payment.captured / bill.finalized to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("billing service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
