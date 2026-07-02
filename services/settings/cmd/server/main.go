// Command server is the composition root for the settings service: load config,
// open Postgres, run embedded migrations, wire adapters -> app (repos + the
// in-process TTL cache + the tenancy role reader), mount the Connect handler with
// interceptors, start the outbox relay, and serve with graceful shutdown.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/settings/v1/settingsv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	cacheadapter "github.com/restorna/platform/services/settings/internal/adapters/cache"
	grpcadapter "github.com/restorna/platform/services/settings/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/settings/internal/adapters/pg"
	"github.com/restorna/platform/services/settings/internal/app"
	"github.com/restorna/platform/services/settings/migrations"
)

// Config extends the shared base with settings-specific knobs.
type Config struct {
	config.Base
	// CacheTTLSecs controls how long an effective value stays in the in-process
	// cache before re-resolution. Invalidation on SetOverride is immediate
	// regardless. (Seconds, because pkg/config parses plain integers.)
	CacheTTLSecs int `env:"SETTINGS_CACHE_TTL_SECS" default:"30"`
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

	// Adapters -> ports. Repo implements both DefinitionRepo and OverrideRepo.
	repo := pgadapter.New(pool)
	cache := cacheadapter.New(time.Duration(cfg.CacheTTLSecs) * time.Second)

	uc := app.New(repo, repo, cache, grpcadapter.RoleFromCtx)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := settingsv1connect.NewSettingsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Drain the outbox to NATS (override.changed.v1 events).
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("settings service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
