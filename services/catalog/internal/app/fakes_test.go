package app_test

import (
	"context"
	"errors"
	"sync"

	"github.com/restorna/platform/services/catalog/internal/domain"
	"github.com/restorna/platform/services/catalog/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). Items are
// stored brand-level; overrides are keyed by item id (single-outlet test scope).
type fakeRepo struct {
	mu         sync.Mutex
	categories map[string]domain.Category
	items      map[string]domain.Item
	overrides  map[string]domain.OutletOverride
	events     []stagedEvent
	failWrite  bool // when true, writes return an error (tests rollback)
}

type stagedEvent struct {
	Type     string
	TenantID string
	Data     any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		categories: map[string]domain.Category{},
		items:      map[string]domain.Item{},
		overrides:  map[string]domain.OutletOverride{},
	}
}

func (r *fakeRepo) Atomic(ctx context.Context, _ string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r}
	if err := fn(tx); err != nil {
		return err
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) ListCategories(_ context.Context, _ string) ([]domain.Category, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Category, 0, len(r.categories))
	for _, c := range r.categories {
		out = append(out, c)
	}
	return out, nil
}

func (r *fakeRepo) GetItem(_ context.Context, _, itemID string) (domain.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	it, ok := r.items[itemID]
	if !ok {
		return domain.Item{}, domain.ErrNotFound
	}
	if ov, ok := r.overrides[itemID]; ok {
		return it.Effective(&ov), nil
	}
	return it, nil
}

func (r *fakeRepo) ListItems(_ context.Context, _ string) ([]domain.Item, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Item, 0, len(r.items))
	for _, it := range r.items {
		if ov, ok := r.overrides[it.ID]; ok {
			out = append(out, it.Effective(&ov))
		} else {
			out = append(out, it)
		}
	}
	return out, nil
}

// fakeTx is the unit-of-work over fakeRepo (already holds the repo lock via Atomic).
type fakeTx struct {
	repo   *fakeRepo
	staged []stagedEvent
}

func (t *fakeTx) UpsertCategory(_ context.Context, c domain.Category) error {
	if t.repo.failWrite {
		return errors.New("write failed")
	}
	t.repo.categories[c.ID] = c
	return nil
}

func (t *fakeTx) UpsertItem(_ context.Context, it domain.Item) error {
	if t.repo.failWrite {
		return errors.New("write failed")
	}
	t.repo.items[it.ID] = it
	return nil
}

func (t *fakeTx) GetItem(_ context.Context, itemID string) (domain.Item, error) {
	it, ok := t.repo.items[itemID]
	if !ok {
		return domain.Item{}, domain.ErrNotFound
	}
	return it, nil
}

func (t *fakeTx) GetOverride(_ context.Context, itemID string) (domain.OutletOverride, bool, error) {
	ov, ok := t.repo.overrides[itemID]
	return ov, ok, nil
}

func (t *fakeTx) PutOverride(_ context.Context, ov domain.OutletOverride) error {
	if t.repo.failWrite {
		return errors.New("write failed")
	}
	t.repo.overrides[ov.ItemID] = ov
	return nil
}

func (t *fakeTx) ClearOverride(_ context.Context, itemID string) error {
	delete(t.repo.overrides, itemID)
	return nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, tenantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, TenantID: tenantID, Data: data})
	return nil
}
