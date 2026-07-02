//go:build integration

// Integration tests for the Postgres adapter. Run in CI against a real Postgres
// (testcontainer or docker-compose), not required locally:
//
//	TEST_DATABASE_URL=postgres://restorna:restorna@localhost:5432/restorna?sslmode=disable \
//	  go test -tags=integration ./internal/adapters/pg/...
//
// These exercise the transactional + idempotent guarantees that the in-memory
// fakes only approximate: FOR UPDATE serialisation and reservation dedupe.
package pg

import (
	"context"
	"embed"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/pg"
	"github.com/restorna/platform/services/entitlements/internal/domain"
)

//go:embed all:../../../migrations/*.sql
var migrationsFS embed.FS

func setup(t *testing.T) *Repo {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	if err := pg.Migrate(dsn, migrationsFS); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pg.Open(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return New(pool)
}

func TestPG_SeedPlansPresent(t *testing.T) {
	r := setup(t)
	for _, id := range []string{"free", "growth", "pro", "enterprise"} {
		p, err := r.GetPlan(context.Background(), id)
		if err != nil {
			t.Fatalf("GetPlan(%s): %v", id, err)
		}
		if p.ID != id {
			t.Errorf("plan id = %q, want %q", p.ID, id)
		}
	}
	if _, err := r.GetPlan(context.Background(), "missing"); err != domain.ErrPlanNotFound {
		t.Errorf("missing plan err = %v, want ErrPlanNotFound", err)
	}
}

func TestPG_ReserveIdempotentAndAtomic(t *testing.T) {
	r := setup(t)
	ctx := context.Background()
	owner := "own_pgtest_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	const key = "staff.waiter"
	const limit = int64(3)

	// Idempotent: same reservation id applied many times counts once.
	for i := 0; i < 5; i++ {
		if _, err := r.Reserve(ctx, owner, key, 1, limit, "rsv_dup"); err != nil {
			t.Fatalf("reserve %d: %v", i, err)
		}
	}
	used, _ := r.Used(ctx, owner, key)
	if used != 1 {
		t.Fatalf("used = %d, want 1 after idempotent replays", used)
	}

	// Concurrent distinct reservers cannot exceed the limit.
	var wg sync.WaitGroup
	exceeded := 0
	var mu sync.Mutex
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := r.Reserve(ctx, owner, key, 1, limit, ids(i))
			if err == domain.ErrQuotaExceeded {
				mu.Lock()
				exceeded++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	used, _ = r.Used(ctx, owner, key)
	if used != limit {
		t.Fatalf("used = %d, want %d (limit not breached under concurrency)", used, limit)
	}
	if exceeded == 0 {
		t.Error("expected some reservations to be rejected as over-limit")
	}
}

func ids(i int) string { return "rsv_c_" + string(rune('a'+i)) }
