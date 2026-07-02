package app_test

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/restorna/platform/services/tenant/internal/domain"
	"github.com/restorna/platform/services/tenant/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB).
type fakeRepo struct {
	mu          sync.Mutex
	owners      map[string]domain.Owner
	brands      map[string]domain.Brand
	restaurants map[string]domain.Restaurant
	events      []stagedEvent
	failInsert  bool // when true, InsertBrand/InsertRestaurant return an error (tests compensation)
}

type stagedEvent struct {
	Type    string
	OwnerID string
	Data    any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		owners:      map[string]domain.Owner{},
		brands:      map[string]domain.Brand{},
		restaurants: map[string]domain.Restaurant{},
	}
}

func (r *fakeRepo) Atomic(ctx context.Context, _ string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Snapshot to roll back on error (transaction semantics).
	tx := &fakeTx{repo: r}
	if err := fn(tx); err != nil {
		return err
	}
	// commit staged events
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) GetOwner(_ context.Context, id string) (domain.Owner, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.owners[id]
	if !ok {
		return domain.Owner{}, domain.ErrNotFound
	}
	return o, nil
}

func (r *fakeRepo) ListOwners(_ context.Context, query string, limit, offset int) ([]domain.Owner, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []domain.Owner
	for _, o := range r.owners {
		if query == "" || strings.Contains(strings.ToLower(o.Name), strings.ToLower(query)) {
			all = append(all, o)
		}
	}
	return paginateOwners(all, limit, offset), len(all), nil
}

func (r *fakeRepo) GetBrand(_ context.Context, _ string, brandID string) (domain.Brand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.brands[brandID]
	if !ok {
		return domain.Brand{}, domain.ErrNotFound
	}
	return b, nil
}

func (r *fakeRepo) GetRestaurant(_ context.Context, _, id string) (domain.Restaurant, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rr, ok := r.restaurants[id]
	if !ok {
		return domain.Restaurant{}, domain.ErrNotFound
	}
	return rr, nil
}

func (r *fakeRepo) ListBrands(_ context.Context, ownerID string, limit, offset int) ([]domain.Brand, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []domain.Brand
	for _, b := range r.brands {
		if b.OwnerID == ownerID {
			all = append(all, b)
		}
	}
	return paginate(all, limit, offset), len(all), nil
}

func (r *fakeRepo) ListRestaurants(_ context.Context, _, brandID string, limit, offset int) ([]domain.Restaurant, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []domain.Restaurant
	for _, rr := range r.restaurants {
		if rr.BrandID == brandID {
			all = append(all, rr)
		}
	}
	return paginateR(all, limit, offset), len(all), nil
}

// fakeTx is the unit-of-work over fakeRepo (already holds the repo lock via Atomic).
type fakeTx struct {
	repo   *fakeRepo
	staged []stagedEvent
}

func (t *fakeTx) InsertOwner(_ context.Context, o domain.Owner) error {
	t.repo.owners[o.ID] = o
	return nil
}

func (t *fakeTx) InsertBrand(_ context.Context, b domain.Brand) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.repo.brands[b.ID] = b
	return nil
}

func (t *fakeTx) InsertRestaurant(_ context.Context, r domain.Restaurant) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.repo.restaurants[r.ID] = r
	return nil
}

func (t *fakeTx) UpdateBrand(_ context.Context, b domain.Brand) error {
	if _, ok := t.repo.brands[b.ID]; !ok {
		return domain.ErrNotFound
	}
	t.repo.brands[b.ID] = b
	return nil
}

func (t *fakeTx) UpdateRestaurant(_ context.Context, r domain.Restaurant) error {
	if _, ok := t.repo.restaurants[r.ID]; !ok {
		return domain.ErrNotFound
	}
	t.repo.restaurants[r.ID] = r
	return nil
}

func (t *fakeTx) GetBrand(_ context.Context, brandID string) (domain.Brand, error) {
	b, ok := t.repo.brands[brandID]
	if !ok {
		return domain.Brand{}, domain.ErrNotFound
	}
	return b, nil
}

func (t *fakeTx) GetRestaurant(_ context.Context, id string) (domain.Restaurant, error) {
	rr, ok := t.repo.restaurants[id]
	if !ok {
		return domain.Restaurant{}, domain.ErrNotFound
	}
	return rr, nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, ownerID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, OwnerID: ownerID, Data: data})
	return nil
}

// fakeEntitlements records reservation calls and enforces a per-key limit so tests
// can assert quota is reserved AND that over-limit blocks.
type fakeEntitlements struct {
	mu sync.Mutex

	limits   map[string]int64 // key -> allowed count (-1 = unlimited)
	used     map[string]int64 // key -> current count
	features map[string]bool

	reserveCalls []reserveCall
	releaseCalls []reserveCall
	hint         string
	reserveErr   error
}

type reserveCall struct {
	OwnerID, Key, ReservationID string
	Delta                       int64
}

func newFakeEntitlements() *fakeEntitlements {
	return &fakeEntitlements{
		limits:   map[string]int64{},
		used:     map[string]int64{},
		features: map[string]bool{},
		hint:     "Upgrade to Growth for more.",
	}
}

func (f *fakeEntitlements) ReserveQuota(_ context.Context, ownerID, key string, delta int64, reservationID string) (ports.ReserveResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reserveCalls = append(f.reserveCalls, reserveCall{ownerID, key, reservationID, delta})
	if f.reserveErr != nil {
		return ports.ReserveResult{}, f.reserveErr
	}
	limit, ok := f.limits[key]
	if !ok {
		// default: 1 slot so the first create succeeds, the second is blocked.
		limit = 1
	}
	if limit == -1 || f.used[key]+delta <= limit {
		f.used[key] += delta
		rem := int64(-1)
		if limit != -1 {
			rem = limit - f.used[key]
		}
		return ports.ReserveResult{OK: true, Remaining: rem}, nil
	}
	return ports.ReserveResult{OK: false, Remaining: 0, UpgradeHint: f.hint}, nil
}

func (f *fakeEntitlements) ReleaseQuota(_ context.Context, ownerID, key string, delta int64, reservationID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls = append(f.releaseCalls, reserveCall{ownerID, key, reservationID, delta})
	f.used[key] -= delta
	return nil
}

func (f *fakeEntitlements) HasFeature(_ context.Context, _, feature string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.features[feature], nil
}

// fakeBlob is an in-memory ports.BlobStore.
type fakeBlob struct {
	puts int
	err  error
}

func (b *fakeBlob) Put(_ context.Context, data []byte, contentType string) (domain.Asset, error) {
	if b.err != nil {
		return domain.Asset{}, b.err
	}
	b.puts++
	return domain.Asset{ID: "ast_test", URL: "https://cdn.test/ast_test", ContentType: contentType}, nil
}

func paginateOwners(in []domain.Owner, limit, offset int) []domain.Owner {
	if offset >= len(in) {
		return nil
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}

func paginate(in []domain.Brand, limit, offset int) []domain.Brand {
	if offset >= len(in) {
		return nil
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}

func paginateR(in []domain.Restaurant, limit, offset int) []domain.Restaurant {
	if offset >= len(in) {
		return nil
	}
	end := offset + limit
	if end > len(in) {
		end = len(in)
	}
	return in[offset:end]
}
