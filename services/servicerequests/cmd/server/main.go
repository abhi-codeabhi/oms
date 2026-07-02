// Command server is the composition root for the service-requests service. It
// wires adapters to the app, starts the Connect server and the outbox relay
// (CONVENTIONS.md main.go skeleton). The settings client supplies the cooldown +
// escalation thresholds; it degrades to defaults when settings is unavailable.
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/servicerequests/v1/servicerequestsv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	grpcadapter "github.com/restorna/platform/services/servicerequests/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/servicerequests/internal/adapters/pg"
	settingsclient "github.com/restorna/platform/services/servicerequests/internal/adapters/settings"
	"github.com/restorna/platform/services/servicerequests/internal/app"
	"github.com/restorna/platform/services/servicerequests/migrations"
)

// Config extends the shared base with service-requests-specific knobs.
type Config struct {
	config.Base
	SettingsURL string `env:"SETTINGS_URL" default:"http://settings:8080"`
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
	settings := settingsclient.New(http.DefaultClient, cfg.SettingsURL)

	uc := app.New(repo, settings, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := servicerequestsv1connect.NewServiceRequestsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Outbox relay: drain raised / escalated events to NATS.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("servicerequests service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
