// Command server is the composition root for the entitlements service: load
// config, open Postgres, run embedded migrations, wire adapters -> app, mount the
// Connect handler with interceptors, and serve with graceful shutdown.
package main

import (
	"context"
	"embed"
	"log"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"

	"github.com/restorna/platform/gen/go/restorna/entitlements/v1/entitlementsv1connect"
	"github.com/restorna/platform/pkg/config"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/pg"

	"github.com/restorna/platform/services/entitlements/internal/app"
	grpcadapter "github.com/restorna/platform/services/entitlements/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/entitlements/internal/adapters/pg"
)

//go:embed all:../../migrations/*.sql
var migrationsFS embed.FS

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load[config.Base]()
	if err != nil {
		log.Fatalf("entitlements: load config: %v", err)
	}

	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("entitlements: open db: %v", err)
	}
	defer pool.Close()

	if err := pg.Migrate(cfg.DatabaseURL, migrationsFS); err != nil {
		log.Fatalf("entitlements: migrate: %v", err)
	}

	repo := pgadapter.New(pool)
	uc := app.New(repo, repo, repo) // Repo implements all three ports.

	srv := grpcx.NewServer(cfg)
	path, handler := entitlementsv1connect.NewEntitlementsServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	if err := srv.Run(ctx); err != nil {
		log.Fatalf("entitlements: server: %v", err)
	}
}
