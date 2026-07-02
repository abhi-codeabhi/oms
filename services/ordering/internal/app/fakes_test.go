package app_test

import (
	"context"
	"sort"
	"sync"

	"github.com/restorna/platform/services/ordering/internal/domain"
	"github.com/restorna/platform/services/ordering/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). It models the
// transactional outbox: events staged inside Atomic commit only if fn succeeds.
type fakeRepo struct {
	mu         sync.Mutex
	orders     map[string]domain.Order
	events     []stagedEvent
	failInsert bool // when true, InsertOrder errors (tests rollback)
}

type stagedEvent struct {
	Type     string
	TenantID string
	Data     any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{orders: map[string]domain.Order{}}
}

func (r *fakeRepo) Atomic(_ context.Context, _ string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r}
	if err := fn(tx); err != nil {
		return err // staged events discarded -> rollback
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) GetOrder(_ context.Context, restaurantID, orderID string) (domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.orders[orderID]
	if !ok || o.RestaurantID != restaurantID {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, nil
}

func (r *fakeRepo) ListForRestaurant(_ context.Context, restaurantID string) ([]domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return collect(r.orders, restaurantID), nil
}

// fakeTx is the unit-of-work over fakeRepo (already holds the repo lock via Atomic).
type fakeTx struct {
	repo   *fakeRepo
	staged []stagedEvent
}

func (t *fakeTx) InsertOrder(_ context.Context, o domain.Order) error {
	if t.repo.failInsert {
		return errInsert
	}
	t.repo.orders[o.ID] = o
	return nil
}

func (t *fakeTx) SetBilled(_ context.Context, orderID string, billed bool) error {
	o, ok := t.repo.orders[orderID]
	if !ok {
		return domain.ErrNotFound
	}
	o.Billed = billed
	t.repo.orders[orderID] = o
	return nil
}

func (t *fakeTx) SetTable(_ context.Context, orderID, tableID string) error {
	o, ok := t.repo.orders[orderID]
	if !ok {
		return domain.ErrNotFound
	}
	o.TableID = tableID
	t.repo.orders[orderID] = o
	return nil
}

func (t *fakeTx) GetOrder(_ context.Context, orderID string) (domain.Order, error) {
	o, ok := t.repo.orders[orderID]
	if !ok {
		return domain.Order{}, domain.ErrNotFound
	}
	return o, nil
}

func (t *fakeTx) ListForRestaurant(_ context.Context, restaurantID string) ([]domain.Order, error) {
	return collect(t.repo.orders, restaurantID), nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, tenantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, TenantID: tenantID, Data: data})
	return nil
}

// collect returns the restaurant's orders sorted newest-first (deterministic by id).
func collect(m map[string]domain.Order, restaurantID string) []domain.Order {
	var out []domain.Order
	for _, o := range m {
		if o.RestaurantID == restaurantID {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

type sentinel string

func (s sentinel) Error() string { return string(s) }

const errInsert = sentinel("insert failed")
