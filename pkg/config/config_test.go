package config

import "testing"

func TestLoadDefaults(t *testing.T) {
	// Ensure no env overrides leak in.
	for _, k := range []string{"PORT", "DATABASE_URL", "NATS_URL", "JWT_PUBLIC_KEY", "OTEL_EXPORTER_OTLP_ENDPOINT", "APP_ENV"} {
		t.Setenv(k, "")
	}
	cfg, err := Load[Base]()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "8080" {
		t.Fatalf("Port default = %q, want 8080", cfg.Port)
	}
	if cfg.Env != "dev" {
		t.Fatalf("Env default = %q, want dev", cfg.Env)
	}
}

func TestLoadFromEnv(t *testing.T) {
	t.Setenv("PORT", "9090")
	t.Setenv("DATABASE_URL", "postgres://localhost/db")
	t.Setenv("NATS_URL", "nats://localhost:4222")
	t.Setenv("APP_ENV", "prod")

	cfg, err := Load[Base]()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Port != "9090" {
		t.Fatalf("Port = %q, want 9090", cfg.Port)
	}
	if cfg.DatabaseURL != "postgres://localhost/db" {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.NatsURL != "nats://localhost:4222" {
		t.Fatalf("NatsURL = %q", cfg.NatsURL)
	}
	if cfg.Env != "prod" {
		t.Fatalf("Env = %q, want prod", cfg.Env)
	}
}

func TestLoadEmbedAndTypes(t *testing.T) {
	type Svc struct {
		Base
		Workers int  `env:"WORKERS" default:"4"`
		Debug   bool `env:"DEBUG" default:"false"`
	}
	t.Setenv("WORKERS", "8")
	t.Setenv("DEBUG", "true")
	t.Setenv("PORT", "7000")

	cfg, err := Load[Svc]()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Workers != 8 {
		t.Fatalf("Workers = %d, want 8", cfg.Workers)
	}
	if !cfg.Debug {
		t.Fatalf("Debug = false, want true")
	}
	if cfg.Port != "7000" {
		t.Fatalf("embedded Base Port = %q, want 7000", cfg.Port)
	}
}
