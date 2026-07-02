// Command server is the composition root for the gateway edge service. It builds
// the downstream Connect clients (one per control-plane service), wires the BFF
// route groups behind CORS + auth + rate-limit + logging middleware, and serves
// Connect/gRPC-Web/JSON over h2c via pkg/grpcx (CONVENTIONS.md main.go skeleton).
package main

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"github.com/rs/zerolog/log"
	"golang.org/x/net/http2"

	"github.com/restorna/platform/pkg/config"
	"github.com/restorna/platform/pkg/grpcx"

	"github.com/restorna/platform/services/gateway/internal/bff"
	"github.com/restorna/platform/services/gateway/internal/clients"
	"github.com/restorna/platform/services/gateway/internal/middleware"
)

// Config extends the shared base with the gateway's downstream URLs and edge knobs.
type Config struct {
	config.Base

	IdentityURL     string `env:"IDENTITY_URL" default:"http://identity:8080"`
	TenantURL       string `env:"TENANT_URL" default:"http://tenant:8080"`
	EntitlementsURL string `env:"ENTITLEMENTS_URL" default:"http://entitlements:8080"`
	StaffURL        string `env:"STAFF_URL" default:"http://staff:8080"`
	SettingsURL     string `env:"SETTINGS_URL" default:"http://settings:8080"`
	OnboardingURL   string `env:"ONBOARDING_URL" default:"http://onboarding:8080"`

	CORSOrigins string `env:"CORS_ALLOWED_ORIGINS" default:"*"`
	// Rate limit is whole requests/sec + burst (pkg/config only parses ints).
	RateLimitRPS   int `env:"RATE_LIMIT_RPS" default:"20"`
	RateLimitBurst int `env:"RATE_LIMIT_BURST" default:"40"`
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	cfg, err := config.Load[Config]()
	if err != nil {
		log.Fatal().Err(err).Msg("load config")
	}

	// h2c client so the gateway can speak gRPC/Connect to the backend services over
	// cleartext HTTP/2 (the same transport pkg/grpcx serves).
	httpClient := h2cClient()

	// Downstream clients: forward the caller's verified bearer token + standard
	// observability interceptors on every call.
	set := clients.New(httpClient, clients.URLs{
		Identity:     cfg.IdentityURL,
		Tenant:       cfg.TenantURL,
		Entitlements: cfg.EntitlementsURL,
		Staff:        cfg.StaffURL,
		Settings:     cfg.SettingsURL,
		Onboarding:   cfg.OnboardingURL,
	}, connect.WithInterceptors(clients.ForwardAuth()))

	// Edge middleware.
	authmw := middleware.NewAuth(cfg.JWTPubKey)
	cors := middleware.NewCORS(cfg.CORSOrigins)
	rl := middleware.NewRateLimit(middleware.NewTokenBucket(float64(cfg.RateLimitRPS), float64(cfg.RateLimitBurst)))

	// BFF routes.
	apiMux := http.NewServeMux()
	bff.NewRouter(bff.New(set), authmw).Mount(apiMux)

	// Middleware order (outermost first): logging -> CORS -> rate limit -> routes.
	// (Per-route auth + role gates are applied inside the router.)
	handler := middleware.Logging(cors.Wrap(rl.Wrap(apiMux)))

	// Serve over the shared grpcx server (h2c + /healthz + /readyz + graceful stop).
	srv := grpcx.NewServer(cfg.Base)
	srv.Mount("/api/", handler)

	log.Info().Str("port", cfg.Port).Msg("gateway service starting")
	if err := srv.Run(ctx); err != nil {
		log.Fatal().Err(err).Msg("server exited")
		os.Exit(1)
	}
}

// h2cClient returns an *http.Client whose transport speaks HTTP/2 cleartext (h2c),
// matching the h2c servers exposed by the backend services via pkg/grpcx.
func h2cClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http2.Transport{
			AllowHTTP: true,
			// Dial plain TCP even though the scheme is http:// (h2c, no TLS).
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, network, addr)
			},
		},
	}
}
