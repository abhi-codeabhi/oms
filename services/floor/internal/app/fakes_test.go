package app_test

import (
	"context"
	"sync"

	"github.com/restorna/platform/services/floor/internal/domain"
	"github.com/restorna/platform/services/floor/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB), partitioned by
// restaurantID so tenant isolation is observable.
type fakeRepo struct {
	mu        sync.Mutex
	floors    map[string]domain.Floor // restaurantID -> floor doc
	events    []stagedEvent
	processed map[string]struct{} // restaurantID|eventID
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{floors: map[string]domain.Floor{}, processed: map[string]struct{}{}}
}

func (r *fakeRepo) Get(_ context.Context, restaurantID string) (domain.Floor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	f, ok := r.floors[restaurantID]
	if !ok {
		return domain.Floor{}, domain.ErrNotFound
	}
	return cloneFloor(f), nil
}

func (r *fakeRepo) Save(_ context.Context, restaurantID string, f domain.Floor) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.floors[restaurantID] = cloneFloor(f)
	return nil
}

func (r *fakeRepo) Atomic(_ context.Context, restaurantID string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, restaurantID: restaurantID}
	if err := fn(tx); err != nil {
		return err // staged writes discarded (rollback)
	}
	if tx.saved != nil {
		r.floors[restaurantID] = cloneFloor(*tx.saved)
	}
	for k := range tx.markProcessed {
		r.processed[k] = struct{}{}
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo          *fakeRepo
	restaurantID  string
	saved         *domain.Floor
	staged        []stagedEvent
	markProcessed map[string]struct{}
}

func (t *fakeTx) Get(_ context.Context, restaurantID string) (domain.Floor, error) {
	if t.saved != nil {
		return cloneFloor(*t.saved), nil
	}
	f, ok := t.repo.floors[restaurantID]
	if !ok {
		return domain.Floor{}, domain.ErrNotFound
	}
	return cloneFloor(f), nil
}

func (t *fakeTx) Save(_ context.Context, _ string, f domain.Floor) error {
	c := cloneFloor(f)
	t.saved = &c
	return nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, restaurantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, RestaurantID: restaurantID, Data: data})
	return nil
}

func (t *fakeTx) MarkProcessed(_ context.Context, restaurantID, eventID string) error {
	if eventID == "" {
		return nil
	}
	if t.markProcessed == nil {
		t.markProcessed = map[string]struct{}{}
	}
	t.markProcessed[restaurantID+"|"+eventID] = struct{}{}
	return nil
}

func (t *fakeTx) Seen(_ context.Context, restaurantID, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	_, ok := t.repo.processed[restaurantID+"|"+eventID]
	return ok, nil
}

func cloneFloor(f domain.Floor) domain.Floor {
	c := domain.Floor{ID: f.ID, Tables: make([]domain.Table, len(f.Tables))}
	copy(c.Tables, f.Tables)
	return c
}

// --- fake clients ---

// fakeKitchen returns canned board/queue tickets keyed by restaurant.
type fakeKitchen struct {
	board map[string][]ports.KitchenTicket
	queue map[string][]ports.KitchenTicket
}

func (k *fakeKitchen) Board(_ context.Context, rid string) ([]ports.KitchenTicket, error) {
	return k.board[rid], nil
}
func (k *fakeKitchen) ServeQueue(_ context.Context, rid string) ([]ports.KitchenTicket, error) {
	return k.queue[rid], nil
}

// fakeBilling returns canned open bills keyed by restaurant.
type fakeBilling struct {
	open map[string][]ports.OpenBill
}

func (b *fakeBilling) ListOpen(_ context.Context, rid string) ([]ports.OpenBill, error) {
	return b.open[rid], nil
}

// fakeSettings returns a fixed nudge config (or default).
type fakeSettings struct {
	cfg *domain.NudgeConfig
}

func (s *fakeSettings) NudgeConfig(_ context.Context, _ string) (domain.NudgeConfig, error) {
	if s.cfg != nil {
		return *s.cfg, nil
	}
	return domain.DefaultNudgeConfig(), nil
}

// fakeOrdering records Relocate calls so tests can assert orders followed the seat.
type fakeOrdering struct {
	calls []relocateCall
}

type relocateCall struct{ From, To string }

func (o *fakeOrdering) Relocate(_ context.Context, _ string, from, to string) (int, error) {
	o.calls = append(o.calls, relocateCall{From: from, To: to})
	return 1, nil
}

func countEvents(repo *fakeRepo, typ string) int {
	n := 0
	for _, e := range repo.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}
