package app_test

import (
	"context"
	"sync"

	"github.com/restorna/platform/services/aggregators/internal/domain"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). Partitioned by
// restaurantID so tenant isolation is observable.
type fakeRepo struct {
	mu        sync.Mutex
	orders    map[string]map[string]domain.ExternalOrder // rid -> id -> order
	events    []stagedEvent
	processed map[string]struct{} // rid|eventID
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		orders:    map[string]map[string]domain.ExternalOrder{},
		processed: map[string]struct{}{},
	}
}

func (r *fakeRepo) bucket(rid string) map[string]domain.ExternalOrder {
	if r.orders[rid] == nil {
		r.orders[rid] = map[string]domain.ExternalOrder{}
	}
	return r.orders[rid]
}

func (r *fakeRepo) Atomic(_ context.Context, rid string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, rid: rid}
	if err := fn(tx); err != nil {
		return err // rollback: discard buffered writes
	}
	for id, o := range tx.writes {
		r.bucket(rid)[id] = o
	}
	for k := range tx.marks {
		r.processed[k] = struct{}{}
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) Get(_ context.Context, rid, id string) (domain.ExternalOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	o, ok := r.bucket(rid)[id]
	if !ok {
		return domain.ExternalOrder{}, domain.ErrNotFound
	}
	return o, nil
}

func (r *fakeRepo) List(_ context.Context, rid, connectorID, status string) ([]domain.ExternalOrder, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.ExternalOrder
	for _, o := range r.bucket(rid) {
		if connectorID != "" && o.ConnectorID != connectorID {
			continue
		}
		if status != "" && string(o.Status) != status {
			continue
		}
		out = append(out, o)
	}
	return out, nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo   *fakeRepo
	rid    string
	writes map[string]domain.ExternalOrder
	staged []stagedEvent
	marks  map[string]struct{}
}

func (t *fakeTx) current(id string) (domain.ExternalOrder, bool) {
	if t.writes != nil {
		if w, ok := t.writes[id]; ok {
			return w, true
		}
	}
	o, ok := t.repo.bucket(t.rid)[id]
	return o, ok
}

func (t *fakeTx) Get(_ context.Context, id string) (domain.ExternalOrder, error) {
	if o, ok := t.current(id); ok {
		return o, nil
	}
	return domain.ExternalOrder{}, domain.ErrNotFound
}

func (t *fakeTx) GetByRef(_ context.Context, connectorID, ref string) (domain.ExternalOrder, error) {
	// Scan committed + buffered writes for a matching (connector, ref).
	for _, o := range t.repo.bucket(t.rid) {
		if o.ConnectorID == connectorID && o.ExternalRef == ref {
			return o, nil
		}
	}
	for _, o := range t.writes {
		if o.ConnectorID == connectorID && o.ExternalRef == ref {
			return o, nil
		}
	}
	return domain.ExternalOrder{}, domain.ErrNotFound
}

func (t *fakeTx) Insert(_ context.Context, o domain.ExternalOrder) error {
	t.put(o)
	return nil
}

func (t *fakeTx) Update(_ context.Context, o domain.ExternalOrder) error {
	t.put(o)
	return nil
}

func (t *fakeTx) put(o domain.ExternalOrder) {
	if t.writes == nil {
		t.writes = map[string]domain.ExternalOrder{}
	}
	t.writes[o.ID] = o
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, rid string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, RestaurantID: rid, Data: data})
	return nil
}

func (t *fakeTx) MarkProcessed(_ context.Context, rid, eventID string) error {
	if eventID == "" {
		return nil
	}
	if t.marks == nil {
		t.marks = map[string]struct{}{}
	}
	t.marks[rid+"|"+eventID] = struct{}{}
	return nil
}

func (t *fakeTx) Seen(_ context.Context, rid, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	if t.marks != nil {
		if _, ok := t.marks[rid+"|"+eventID]; ok {
			return true, nil
		}
	}
	_, ok := t.repo.processed[rid+"|"+eventID]
	return ok, nil
}

// fakeCatalog implements ports.Catalog.
type fakeCatalog struct {
	items map[string][]ports.MenuItem // rid -> items
	err   error
}

func newFakeCatalog() *fakeCatalog { return &fakeCatalog{items: map[string][]ports.MenuItem{}} }

func (c *fakeCatalog) ListAllItems(_ context.Context, rid string) ([]ports.MenuItem, error) {
	if c.err != nil {
		return nil, c.err
	}
	return c.items[rid], nil
}

// fakeHub implements ports.ConnectorHub, recording the menu it was pushed.
type fakeHub struct {
	resolved   ports.ResolvedConnector
	resolveErr error
	pushErr    error
	pushed     []byte
	pushedRC   ports.ResolvedConnector
	accepted   int
}

func newFakeHub() *fakeHub {
	return &fakeHub{
		resolved: ports.ResolvedConnector{ConnectorID: "mockagg", InstallationID: "inst_1"},
	}
}

func (h *fakeHub) Resolve(_ context.Context, _, preferConnectorID string) (ports.ResolvedConnector, error) {
	if h.resolveErr != nil {
		return ports.ResolvedConnector{}, h.resolveErr
	}
	rc := h.resolved
	if preferConnectorID != "" {
		rc.ConnectorID = preferConnectorID
	}
	return rc, nil
}

func (h *fakeHub) PushMenu(_ context.Context, rc ports.ResolvedConnector, menuJSON []byte) (int, error) {
	if h.pushErr != nil {
		return 0, h.pushErr
	}
	h.pushed = menuJSON
	h.pushedRC = rc
	return h.accepted, nil
}

// fakeOrdering implements ports.Ordering, recording forwarded orders.
type fakeOrdering struct {
	mu     sync.Mutex
	placed []placedOrder
	err    error
	nextID int
}

type placedOrder struct {
	RestaurantID string
	Table        string
	Lines        []ports.OrderLine
}

func newFakeOrdering() *fakeOrdering { return &fakeOrdering{} }

func (o *fakeOrdering) PlaceOrder(_ context.Context, rid, table string, lines []ports.OrderLine) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return "", o.err
	}
	o.placed = append(o.placed, placedOrder{RestaurantID: rid, Table: table, Lines: lines})
	o.nextID++
	return "ord_fake", nil
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
