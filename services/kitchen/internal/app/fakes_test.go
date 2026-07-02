package app_test

import (
	"context"
	"errors"
	"sync"

	"github.com/restorna/platform/services/kitchen/internal/domain"
	"github.com/restorna/platform/services/kitchen/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). It is
// partitioned by restaurantID so tenant isolation is observable in tests.
type fakeRepo struct {
	mu         sync.Mutex
	tickets    map[string]map[string]domain.Ticket // restaurantID -> ticketID -> ticket
	events     []stagedEvent
	processed  map[string]struct{} // restaurantID|eventID
	failInsert bool                // when true, Insert returns an error (tests rollback)
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		tickets:   map[string]map[string]domain.Ticket{},
		processed: map[string]struct{}{},
	}
}

func (r *fakeRepo) bucket(restaurantID string) map[string]domain.Ticket {
	if r.tickets[restaurantID] == nil {
		r.tickets[restaurantID] = map[string]domain.Ticket{}
	}
	return r.tickets[restaurantID]
}

func (r *fakeRepo) Atomic(ctx context.Context, restaurantID string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, restaurantID: restaurantID}
	if err := fn(tx); err != nil {
		return err // staged writes are discarded (rollback)
	}
	for id, t := range tx.writes {
		r.bucket(restaurantID)[id] = t
	}
	for k := range tx.markProcessed {
		r.processed[k] = struct{}{}
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) List(_ context.Context, restaurantID string) ([]domain.Ticket, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Ticket
	for _, t := range r.bucket(restaurantID) {
		out = append(out, t)
	}
	return out, nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo          *fakeRepo
	restaurantID  string
	writes        map[string]domain.Ticket
	staged        []stagedEvent
	markProcessed map[string]struct{}
}

func (t *fakeTx) Get(_ context.Context, ticketID string) (domain.Ticket, error) {
	if t.writes != nil {
		if w, ok := t.writes[ticketID]; ok {
			return w, nil
		}
	}
	tk, ok := t.repo.bucket(t.restaurantID)[ticketID]
	if !ok {
		return domain.Ticket{}, domain.ErrNotFound
	}
	return tk, nil
}

func (t *fakeTx) Insert(_ context.Context, tk domain.Ticket) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.put(tk)
	return nil
}

func (t *fakeTx) Update(_ context.Context, tk domain.Ticket) error {
	t.put(tk)
	return nil
}

func (t *fakeTx) put(tk domain.Ticket) {
	if t.writes == nil {
		t.writes = map[string]domain.Ticket{}
	}
	t.writes[tk.ID] = tk
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

// fakeCatalog resolves item ids to a name/station map; records lookups.
type fakeCatalog struct {
	mu      sync.Mutex
	items   map[string]ports.ResolvedItem // itemID -> resolved
	lookups []string
	err     error
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{items: map[string]ports.ResolvedItem{}}
}

func (c *fakeCatalog) Resolve(_ context.Context, restaurantID, itemID string) (ports.ResolvedItem, error) {
	_ = restaurantID
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lookups = append(c.lookups, itemID)
	if c.err != nil {
		return ports.ResolvedItem{}, c.err
	}
	r, ok := c.items[itemID]
	if !ok {
		return ports.ResolvedItem{}, domain.ErrNotFound
	}
	return r, nil
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
