package app_test

import (
	"context"
	"errors"
	"sync"

	"github.com/restorna/platform/services/onboarding/internal/domain"
	"github.com/restorna/platform/services/onboarding/internal/ports"
)

var errBoom = errors.New("boom")

// fakeRepo is an in-memory ports.Repo. It records staged events and counts
// Create/Save calls so tests can assert idempotency (a resumed step must not
// re-Create) and the outbox contract.
type fakeRepo struct {
	mu       sync.Mutex
	states   map[string]domain.State
	events   []ports.OutboxEvent
	creates  int
	saves    int
	failNext error // if set, the next Create/Save returns this and clears it
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{states: map[string]domain.State{}}
}

func (r *fakeRepo) Create(_ context.Context, s domain.State, publish *ports.OutboxEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.creates++
	r.states[s.ID] = s
	if publish != nil {
		r.events = append(r.events, *publish)
	}
	return nil
}

func (r *fakeRepo) Save(_ context.Context, s domain.State, publish *ports.OutboxEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	if _, ok := r.states[s.ID]; !ok {
		return domain.ErrNotFound
	}
	r.saves++
	r.states[s.ID] = s
	if publish != nil {
		r.events = append(r.events, *publish)
	}
	return nil
}

func (r *fakeRepo) Get(_ context.Context, id string) (domain.State, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.states[id]
	if !ok {
		return domain.State{}, domain.ErrNotFound
	}
	return s, nil
}

func (r *fakeRepo) eventTypes() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.Type
	}
	return out
}

func (r *fakeRepo) createCount() int { r.mu.Lock(); defer r.mu.Unlock(); return r.creates }

// fakeIdentity records EnsureOwnerUser calls.
type fakeIdentity struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeIdentity) EnsureOwnerUser(_ context.Context, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return "usr_fake", nil
}

// fakeTenant records every tenant call and returns deterministic ids.
type fakeTenant struct {
	mu          sync.Mutex
	owners      int
	brands      int
	logos       int
	restaurants int
}

func (f *fakeTenant) CreateOwner(_ context.Context, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.owners++
	return "own_fake", nil
}

func (f *fakeTenant) CreateBrand(_ context.Context, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.brands++
	return "brnd_fake", nil
}

func (f *fakeTenant) SetBrandLogo(_ context.Context, _ string, _ []byte, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.logos++
	return "https://cdn/logo.png", nil
}

func (f *fakeTenant) CreateRestaurant(_ context.Context, _, _, _, _, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restaurants++
	return "out_fake", nil
}

func (f *fakeTenant) counts() (owners, brands, logos, restaurants int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.owners, f.brands, f.logos, f.restaurants
}

// fakeEnts records AssignPlan calls and the last plan assigned.
type fakeEnts struct {
	mu       sync.Mutex
	calls    int
	lastPlan string
}

func (f *fakeEnts) AssignPlan(_ context.Context, _, planID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.lastPlan = planID
	return nil
}

// fakeStaff records AddStaff/InviteStaff. Roles listed in quotaExhausted return
// ports.ErrQuotaExhausted from AddStaff so partial-failure reporting can be
// asserted.
type fakeStaff struct {
	mu              sync.Mutex
	added           []string
	invited         []string
	quotaExhausted  map[string]bool // role -> exhausted
	addFailContains string          // if a name contains this, AddStaff errors
}

func newFakeStaff() *fakeStaff {
	return &fakeStaff{quotaExhausted: map[string]bool{}}
}

func (f *fakeStaff) AddStaff(_ context.Context, _, name, _, _, role string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.quotaExhausted[role] {
		return "", ports.ErrQuotaExhausted
	}
	if f.addFailContains != "" && name == f.addFailContains {
		return "", errBoom
	}
	id := "stf_" + name
	f.added = append(f.added, id)
	return id, nil
}

func (f *fakeStaff) InviteStaff(_ context.Context, staffID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invited = append(f.invited, staffID)
	return "inv_" + staffID, nil
}

func (f *fakeStaff) counts() (added, invited int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.added), len(f.invited)
}

// fakeSettings records SetOverride keys.
type fakeSettings struct {
	mu   sync.Mutex
	keys []string
}

func (f *fakeSettings) SetOverride(_ context.Context, _, _, _, key, _, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys = append(f.keys, key)
	return nil
}

func (f *fakeSettings) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.keys) }
