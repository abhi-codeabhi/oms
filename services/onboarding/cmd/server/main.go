// Command server is the onboarding service composition root: it loads config,
// opens Postgres, runs migrations, dials the five downstream control-plane
// services as Connect clients over h2c, wires them into the saga use cases,
// mounts the Connect handler with the standard interceptors, starts the outbox
// relay, and runs the HTTP/2 server with graceful shutdown.
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

	"github.com/restorna/platform/gen/go/restorna/onboarding/v1/onboardingv1connect"
	"github.com/restorna/platform/pkg/config"
	"github.com/restorna/platform/pkg/eventbus/nats"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/outbox"
	pgx5 "github.com/restorna/platform/pkg/pg"

	"github.com/restorna/platform/services/onboarding/internal/adapters/clients"
	grpcadapter "github.com/restorna/platform/services/onboarding/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/onboarding/internal/adapters/pg"
	"github.com/restorna/platform/services/onboarding/internal/app"
	"github.com/restorna/platform/services/onboarding/migrations"
)

// Config is the onboarding service config: the shared Base (PORT, DATABASE_URL,
// …) plus the URLs of the five downstream services this saga orchestrates.
type Config struct {
	config.Base
	IdentityURL     string `env:"IDENTITY_URL" default:"http://identity:8080"`
	TenantURL       string `env:"TENANT_URL" default:"http://tenant:8080"`
	EntitlementsURL string `env:"ENTITLEMENTS_URL" default:"http://entitlements:8080"`
	StaffURL        string `env:"STAFF_URL" default:"http://staff:8080"`
	SettingsURL     string `env:"SETTINGS_URL" default:"http://settings:8080"`
}

func main() {
	log := zerolog.New(os.Stdout).With().Timestamp().Str("service", "onboarding").Logger()

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

	// One h2c client shared by all outbound service ports (plaintext gRPC
	// in-cluster). Each adapter dials its own base URL from env.
	httpClient := &http.Client{Transport: h2cTransport()}

	identity := clients.NewIdentity(httpClient, cfg.IdentityURL)
	tenant := clients.NewTenant(httpClient, cfg.TenantURL)
	ents := clients.NewEntitlements(httpClient, cfg.EntitlementsURL)
	staff := clients.NewStaff(httpClient, cfg.StaffURL)
	settings := clients.NewSettings(httpClient, cfg.SettingsURL)

	// Ports → saga use cases.
	repo := pgadapter.New(pool)
	uc := app.New(repo, identity, tenant, ents, staff, settings)

	// Connect handler with the standard interceptors.
	srv := grpcx.NewServer(cfg.Base)
	path, handler := onboardingv1connect.NewOnboardingServiceHandler(
		grpcadapter.New(uc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	// Drain the outbox (onboarding.completed.v1) to NATS in the background.
	go func() {
		if err := outbox.Relay(ctx, pool, bus); err != nil && ctx.Err() == nil {
			log.Error().Err(err).Msg("outbox relay stopped")
		}
	}()

	log.Info().Str("port", cfg.Port).Msg("onboarding service listening")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
	}
}

// h2cTransport returns an HTTP/2 transport that talks plaintext (h2c) to
// in-cluster gRPC services without TLS.
func h2cTransport() *http2.Transport {
	return &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
}
