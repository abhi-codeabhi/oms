// Command server is the identity service composition root. It wires config,
// Postgres, migrations, adapters, the Connect handler + interceptors, and runs
// the grpcx server with graceful shutdown — following the INTERFACES skeleton.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"connectrpc.com/connect"

	"github.com/restorna/platform/gen/go/restorna/identity/v1/identityv1connect"
	"github.com/restorna/platform/pkg/config"
	"github.com/restorna/platform/pkg/grpcx"
	"github.com/restorna/platform/pkg/pg"

	authadapter "github.com/restorna/platform/services/identity/internal/adapters/auth"
	grpcadapter "github.com/restorna/platform/services/identity/internal/adapters/grpc"
	pgadapter "github.com/restorna/platform/services/identity/internal/adapters/pg"
	"github.com/restorna/platform/services/identity/internal/adapters/sender"
	"github.com/restorna/platform/services/identity/internal/app"
	"github.com/restorna/platform/services/identity/internal/ports"
	"github.com/restorna/platform/services/identity/migrations"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load[config.Base]()
	if err != nil {
		log.Error("load config", "err", err)
		os.Exit(1)
	}
	devMode := strings.EqualFold(cfg.Env, "dev")

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Ed25519 key pair. Public key is shared with every service's auth
	// interceptor (cfg.JWTPubKey); identity additionally holds the private key
	// to SIGN tokens, supplied via JWT_PRIVATE_KEY (base64 std encoding).
	priv, pub, err := loadKeys(cfg.JWTPubKey, os.Getenv("JWT_PRIVATE_KEY"))
	if err != nil {
		log.Error("load jwt keys", "err", err)
		os.Exit(1)
	}

	pool, err := pg.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Error("open postgres", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	if err := pg.Migrate(cfg.DatabaseURL, migrations.FS); err != nil {
		log.Error("run migrations", "err", err)
		os.Exit(1)
	}

	store := pgadapter.New(pool)
	svc := app.New(app.Config{
		Users:      store.Users(),
		Challenges: store.Challenges(),
		Refresh:    store.Refresh(),
		Sender:     sender.NewLog(log, devMode),
		Signer:     authadapter.New(priv, pub),
		Clock:      ports.RealClock{},
		DevMode:    devMode,
	})

	srv := grpcx.NewServer(cfg)
	path, handler := identityv1connect.NewIdentityServiceHandler(
		grpcadapter.New(svc),
		connect.WithInterceptors(
			grpcx.AuthInterceptor(cfg.JWTPubKey),
			grpcx.LoggingInterceptor(),
			grpcx.OTelInterceptor(),
			grpcx.RecoverInterceptor(),
		),
	)
	srv.Mount(path, handler)

	log.Info("identity service starting", "port", cfg.Port, "dev", devMode)
	if err := srv.Run(ctx); err != nil {
		log.Error("server exited", "err", err)
		os.Exit(1)
	}
}

// loadKeys decodes the base64 std-encoded Ed25519 public + private keys. The
// public key is required (token verification); the private key is required for
// the issuer to mint tokens.
func loadKeys(pubB64, privB64 string) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	pubRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(pubB64))
	if err != nil {
		return nil, nil, err
	}
	privRaw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(privB64))
	if err != nil {
		return nil, nil, err
	}
	return ed25519.PrivateKey(privRaw), ed25519.PublicKey(pubRaw), nil
}
