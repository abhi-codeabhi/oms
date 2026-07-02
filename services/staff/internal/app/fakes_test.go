package app_test

import (
	"context"
	"errors"
	"sync"

	"github.com/restorna/platform/services/staff/internal/domain"
	"github.com/restorna/platform/services/staff/internal/ports"
)

// fakeRepo is an in-memory ports.Repo for unit tests. It records staged events
// so tests can assert the outbox contract without a database.
type fakeRepo struct {
	mu       sync.Mutex
	members  map[string]domain.Member
	events   []ports.OutboxEvent
	failNext error // if set, the next Create/Update returns this and clears it
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{members: map[string]domain.Member{}}
}

func (r *fakeRepo) Create(_ context.Context, _ string, m domain.Member, publish *ports.OutboxEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	r.members[m.ID] = m
	if publish != nil {
		r.events = append(r.events, *publish)
	}
	return nil
}

func (r *fakeRepo) Update(_ context.Context, _ string, m domain.Member, publish *ports.OutboxEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failNext != nil {
		err := r.failNext
		r.failNext = nil
		return err
	}
	if _, ok := r.members[m.ID]; !ok {
		return domain.ErrStaffNotFound
	}
	r.members[m.ID] = m
	if publish != nil {
		r.events = append(r.events, *publish)
	}
	return nil
}

func (r *fakeRepo) Get(_ context.Context, _ string, staffID string) (domain.Member, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.members[staffID]
	if !ok {
		return domain.Member{}, domain.ErrStaffNotFound
	}
	return m, nil
}

func (r *fakeRepo) ListByRestaurant(_ context.Context, _ string, restaurantID string, limit, offset int) ([]domain.Member, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var all []domain.Member
	for _, m := range r.members {
		if m.RestaurantID == restaurantID {
			all = append(all, m)
		}
	}
	// Deterministic order by id.
	sortByID(all)
	total := len(all)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return all[offset:end], total, nil
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

func sortByID(ms []domain.Member) {
	for i := 1; i < len(ms); i++ {
		for j := i; j > 0 && ms[j-1].ID > ms[j].ID; j-- {
			ms[j-1], ms[j] = ms[j], ms[j-1]
		}
	}
}

// reservation tracks a single quota reservation by id.
type reservation struct {
	key   string
	delta int64
}

// fakeEnt is an in-memory ports.Entitlements that enforces a per-key limit so
// tests can prove AddStaff blocks over the cap and release frees a slot.
type fakeEnt struct {
	mu      sync.Mutex
	limits  map[string]int64       // key -> max active reservations (-1 = unlimited)
	hint    string                 // upgrade hint returned when blocked
	active  map[string]reservation // reservationID -> reservation
	usedBy  map[string]int64       // key -> count currently reserved
	failNow error                  // if set, the next Reserve/Release returns it
}

func newFakeEnt(limits map[string]int64, hint string) *fakeEnt {
	return &fakeEnt{
		limits: limits,
		hint:   hint,
		active: map[string]reservation{},
		usedBy: map[string]int64{},
	}
}

func (e *fakeEnt) Reserve(_ context.Context, _ string, key string, delta int64, reservationID string) (bool, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failNow != nil {
		err := e.failNow
		e.failNow = nil
		return false, "", err
	}
	if _, exists := e.active[reservationID]; exists {
		return true, "", nil // idempotent
	}
	limit, ok := e.limits[key]
	if !ok {
		limit = -1 // unknown key => unlimited
	}
	if limit >= 0 && e.usedBy[key]+delta > limit {
		return false, e.hint, nil
	}
	e.active[reservationID] = reservation{key: key, delta: delta}
	e.usedBy[key] += delta
	return true, "", nil
}

func (e *fakeEnt) Release(_ context.Context, _ string, key string, delta int64, reservationID string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failNow != nil {
		err := e.failNow
		e.failNow = nil
		return err
	}
	r, ok := e.active[reservationID]
	if !ok {
		return nil // idempotent
	}
	delete(e.active, reservationID)
	e.usedBy[r.key] -= r.delta
	return nil
}

func (e *fakeEnt) used(key string) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.usedBy[key]
}

// fakeSender records sent invites.
type fakeSender struct {
	mu      sync.Mutex
	sent    []domain.Invite
	failNow error
}

func (s *fakeSender) Send(_ context.Context, inv domain.Invite) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNow != nil {
		return s.failNow
	}
	s.sent = append(s.sent, inv)
	return nil
}

func (s *fakeSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

var errBoom = errors.New("boom")
