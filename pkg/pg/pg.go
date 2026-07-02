// Package pg holds Postgres helpers built on pgx/pgxpool: pool construction, an
// RLS-aware tenant transaction wrapper, and goose-embedded migrations.
package pg

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Open creates a pgx connection pool for dsn and verifies connectivity.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("pg: parse dsn: %w", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pg: connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pg: ping: %w", err)
	}
	return pool, nil
}

// WithTenant runs fn inside a transaction with app.tenant_id set (transaction
// scoped) so Postgres Row-Level Security policies scope rows to tenantID. The tx
// is committed if fn returns nil, otherwise rolled back.
func WithTenant(ctx context.Context, pool *pgxpool.Pool, tenantID string, fn func(pgx.Tx) error) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// true => setting is local to this transaction.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return fmt.Errorf("pg: set tenant: %w", err)
	}
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: commit: %w", err)
	}
	return nil
}

// Migrate applies the goose SQL migrations embedded in fs against dsn. The
// migration files are expected at the root of fs.
func Migrate(dsn string, fs embed.FS) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return fmt.Errorf("pg: open for migrate: %w", err)
	}
	defer db.Close()

	goose.SetBaseFS(fs)
	defer goose.SetBaseFS(nil)

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("pg: dialect: %w", err)
	}
	if err := goose.Up(db, "."); err != nil {
		return fmt.Errorf("pg: migrate up: %w", err)
	}
	return nil
}

// ensure the stdlib driver is registered (imported for its side effect).
var _ = stdlib.GetDefaultDriver
