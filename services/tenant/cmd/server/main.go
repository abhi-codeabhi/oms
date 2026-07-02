// Command server is the composition root for the tenant service. It wires adapters
// to the app and starts the Connect server (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"

	"github.com/restorna/platform/gen/go/restorna/tenant/v1/tenantv1connect"
	"github.com/restorna/platform/pkg/config"
	eventbus "github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	"github.com/restorna/platform/pkg/pg"

	"github.com/restorna/platform/services/tenant/internal/adapters/blob"
	entclient "github.com/restorna/platform/services/tenant/internal/adapters/entitlements"
	grpcadapter "github.com/restorna/platform/services/tenant/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/tenant/internal/adapters/pg"
	"github.com/restorna/platform/services/tenant/internal/app"
	"github.com/restorna/platform/services/tenant/migrations"
)

// Config extends the shared base with tenant-specific knobs.
type Config struct {
	config.Base
	EntitlementsURL string `env:"ENTITLEMENTS_URL" default:"http://entitlements:8080"`
	BlobDir         string `env:"BLOB_DIR" default:"/data/assets"`
	BlobBaseURL     string `env:"BLOB_PUBLIC_BASE_URL" default:""`
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
	ents := entclient.New(http.DefaultClient, cfg.EntitlementsURL)
	blobs, err := blob.NewFilesystem(cfg.BlobDir, cfg.BlobBaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("init blob store")
	}

	uc := app.New(repo, ents, blobs, nil)

	srv := grpcx.NewServer(cfg.Base)
	path, handler := tenantv1connect.NewTenantServiceHandler(
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

	log.Info().Str("port", cfg.Port).Msg("tenant service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}
