package app_test

import (
	"context"
	"sort"
	"sync"

	"github.com/restorna/platform/services/promotions/internal/domain"
	"github.com/restorna/platform/services/promotions/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). Coupons are keyed
// by (restaurantID, code) to mirror the RLS-scoped Postgres primary key.
type fakeRepo struct {
	mu         sync.Mutex
	coupons    map[string]map[string]domain.Coupon // restaurantID -> code -> coupon
	events     []stagedEvent
	failUpsert bool // when true, UpsertCoupon returns an error (tests rollback)
}

type stagedEvent struct {
	Type     string
	TenantID string
	Data     any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{coupons: map[string]map[string]domain.Coupon{}}
}

func (r *fakeRepo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, restaurantID: restaurantID}
	if err := fn(tx); err != nil {
		return err
	}
	// commit staged events only on success (transaction semantics).
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) ListCoupons(_ context.Context, restaurantID string) ([]domain.Coupon, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Coupon
	for _, c := range r.coupons[restaurantID] {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Code < out[j].Code })
	return out, nil
}

// fakeTx is the unit-of-work over fakeRepo (already holds the repo lock via Atomic).
type fakeTx struct {
	repo         *fakeRepo
	restaurantID string
	staged       []stagedEvent
}

func (t *fakeTx) UpsertCoupon(_ context.Context, c domain.Coupon) error {
	if t.repo.failUpsert {
		return errFail
	}
	m, ok := t.repo.coupons[t.restaurantID]
	if !ok {
		m = map[string]domain.Coupon{}
		t.repo.coupons[t.restaurantID] = m
	}
	m[c.Code] = c
	return nil
}

func (t *fakeTx) GetCoupon(_ context.Context, code string) (domain.Coupon, error) {
	c, ok := t.repo.coupons[t.restaurantID][code]
	if !ok {
		return domain.Coupon{}, domain.ErrNotFound
	}
	return c, nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, tenantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, TenantID: tenantID, Data: data})
	return nil
}

// errFail is the synthetic persistence error used by failUpsert.
var errFail = errString("insert failed")

type errString string

func (e errString) Error() string { return string(e) }
