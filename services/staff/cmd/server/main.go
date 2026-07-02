// Command server is the staff service composition root: it loads config, opens
// Postgres, runs migrations, wires the adapters into the app use cases, mounts
// the Connect handler with the standard interceptors, starts the outbox relay,
// and runs the HTTP/2 server with graceful shutdown.
package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"connectrpc.com/connect"
	"github.com/rs/zerolog"
	"golang.org/x/net/http2"

	"github.com/restorna/platform/gen/go/restorna/staff/v1/staffv1connect"
	"github.com/restorna/platform/pkg/config"
	"github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	pgx5 "github.com/restorna/platform/pkg/pg"

	"github.com/restorna/platform/services/staff/internal/adapters/entitlements"
	grpcadapter "github.com/restorna/platform/services/staff/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/staff/internal/adapters/pg"
	"github.com/restorna/platform/services/staff/internal/adapters/invites"
	"github.com/restorna/platform/services/staff/internal/app"
	"github.com/restorna/platform/services/staff/migrations"
)

// Config is the staff service config: the shared Base (PORT, DATABASE_URL, …)
// plus the entitlements service URL this service calls.
type Config struct {
	config.Base
	EntitlementsURL string `env:"ENTITLEMENTS_URL" default:"http://entitlements:8080"`
}

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Str("service", "staff").Logger()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load[Config]()
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	pool, err := pgx5.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("open postgres")
	}
	defer pool.Close()

	if err := pgx5.Migrate(cfg.DatabaseURL, migrations.FS); err != nil {
		log.Fatal().Err(err).Msg("run migrations")
	}

	bus, err := nats.Connect(cfg.NatsURL)
	if err != nil {
		log.Fatal().Err(err).Msg("connect nats")
	}

	// Outbound port: entitlements client over h2c (plaintext gRPC in-cluster).
	entHTTP := &http.Client{Transport: h2cTransport()}
	entClient := entitlements.New(entHTTP, cfg.EntitlementsURL)

	// Ports → app use cases.
	repo := pgadapter.New(pool)
	sender := invites.NewLogSender(log)
	uc := app.New(repo, entClient, sender)

	// Connect handler with the standard interceptors.
	srv := grpcx.NewServer(cfg.Base)
	path, handler := staffv1connect.NewStaffServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Drain the outbox to NATS in the background.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("staff service listening")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
	}
}

// h2cTransport returns an HTTP/2 transport that talks plaintext (h2c) to
// in-cluster gRPC services without TLS.
func h2cTransport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		// Dial plaintext TCP even though the scheme/transport is HTTP/2 ("h2c").
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}
