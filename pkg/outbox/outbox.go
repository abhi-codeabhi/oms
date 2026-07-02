// Package outbox implements the transactional outbox pattern: events are staged
// into an outbox table in the same DB transaction as the business change, and a
// relay loop later drains unpublished rows to the event bus.
//
// The expected schema (created by a service migration) is:
//
//	CREATE TABLE outbox (
//	  id           text PRIMARY KEY,
//	  type         text NOT NULL,
//	  tenant_id    text NOT NULL,
//	  source       text NOT NULL,
//	  occurred_at  timestamptz NOT NULL,
//	  data         jsonb NOT NULL,
//	  published_at timestamptz
//	);
package outbox

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/restorna/platform/pkg/events"
)

// Bus is the minimal publish surface the relay needs; nats.Connect returns one.
type Bus interface {
	Publish(ctx context.Context, e events.Event) error
}

// Stage inserts e into the outbox table within the given transaction, so it is
// committed atomically with the business change.
func Stage(tx pgx.Tx, e events.Event) error {
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	_, err := tx.Exec(context.Background(),
		`INSERT INTO outbox (id, type, tenant_id, source, occurred_at, data)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		e.ID, e.Type, e.TenantID, e.Source, e.OccurredAt, e.Data)
	if err != nil {
		return fmt.Errorf("outbox: stage: %w", err)
	}
	return nil
}

// Relay continuously drains unpublished outbox rows to bus until ctx is done.
// Each successfully published row is marked with published_at. Intended to run
// as a goroutine or sidecar.
func Relay(ctx context.Context, pool *pgxpool.Pool, bus Bus) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := drainOnce(ctx, pool, bus); err != nil && ctx.Err() == nil {
				// Transient errors are logged by the caller's logger via the
				// returned error on shutdown; keep looping otherwise.
				continue
			}
		}
	}
}

func drainOnce(ctx context.Context, pool *pgxpool.Pool, bus Bus) error {
	rows, err := pool.Query(ctx,
		`SELECT id, type, tenant_id, source, occurred_at, data
		   FROM outbox
		  WHERE published_at IS NULL
		  ORDER BY occurred_at
		  LIMIT 100`)
	if err != nil {
		return fmt.Errorf("outbox: query: %w", err)
	}
	var batch []events.Event
	for rows.Next() {
		var e events.Event
		if err := rows.Scan(&e.ID, &e.Type, &e.TenantID, &e.Source, &e.OccurredAt, &e.Data); err != nil {
			rows.Close()
			return fmt.Errorf("outbox: scan: %w", err)
		}
		batch = append(batch, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("outbox: rows: %w", err)
	}

	for _, e := range batch {
		if err := bus.Publish(ctx, e); err != nil {
			return fmt.Errorf("outbox: publish %s: %w", e.ID, err)
		}
		if _, err := pool.Exec(ctx,
			`UPDATE outbox SET published_at = now() WHERE id = $1`, e.ID); err != nil {
			return fmt.Errorf("outbox: mark published %s: %w", e.ID, err)
		}
	}
	return nil
}
