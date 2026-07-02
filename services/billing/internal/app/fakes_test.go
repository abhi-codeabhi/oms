package app_test

import (
	"context"
	"sync"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/billing/internal/domain"
	"github.com/restorna/platform/services/billing/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB), partitioned
// by restaurantID so tenant isolation is observable.
type fakeRepo struct {
	mu        sync.Mutex
	bills     map[string]map[string]domain.Bill // restaurantID -> billID -> bill
	tabs      map[string]map[int32]domain.Tab    // restaurantID -> table -> tab
	events    []stagedEvent
	processed map[string]struct{} // restaurantID|eventID
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		bills:     map[string]map[string]domain.Bill{},
		tabs:      map[string]map[int32]domain.Tab{},
		processed: map[string]struct{}{},
	}
}

func (r *fakeRepo) billBucket(rid string) map[string]domain.Bill {
	if r.bills[rid] == nil {
		r.bills[rid] = map[string]domain.Bill{}
	}
	return r.bills[rid]
}
func (r *fakeRepo) tabBucket(rid string) map[int32]domain.Tab {
	if r.tabs[rid] == nil {
		r.tabs[rid] = map[int32]domain.Tab{}
	}
	return r.tabs[rid]
}

func (r *fakeRepo) Atomic(ctx context.Context, rid string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, rid: rid}
	if err := fn(tx); err != nil {
		return err // staged writes discarded (rollback)
	}
	for id, b := range tx.billWrites {
		r.billBucket(rid)[id] = b
	}
	for tbl, t := range tx.tabWrites {
		r.tabBucket(rid)[tbl] = t
	}
	for tbl := range tx.tabDeletes {
		delete(r.tabBucket(rid), tbl)
	}
	for k := range tx.marked {
		r.processed[k] = struct{}{}
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) GetBill(_ context.Context, rid, billID string) (domain.Bill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b, ok := r.billBucket(rid)[billID]
	if !ok {
		return domain.Bill{}, domain.ErrNotFound
	}
	return b, nil
}

func (r *fakeRepo) ListOpenBills(_ context.Context, rid string) ([]domain.Bill, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Bill
	for _, b := range r.billBucket(rid) {
		if !b.Paid {
			out = append(out, b)
		}
	}
	return out, nil
}

func (r *fakeRepo) ListTabs(_ context.Context, rid string) ([]domain.Tab, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Tab
	for _, t := range r.tabBucket(rid) {
		out = append(out, t)
	}
	return out, nil
}

func (r *fakeRepo) Seen(_ context.Context, rid, eventID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.processed[rid+"|"+eventID]
	return ok, nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo       *fakeRepo
	rid        string
	billWrites map[string]domain.Bill
	tabWrites  map[int32]domain.Tab
	tabDeletes map[int32]struct{}
	staged     []stagedEvent
	marked     map[string]struct{}
}

func (t *fakeTx) GetBill(_ context.Context, billID string) (domain.Bill, error) {
	if t.billWrites != nil {
		if b, ok := t.billWrites[billID]; ok {
			return b, nil
		}
	}
	b, ok := t.repo.billBucket(t.rid)[billID]
	if !ok {
		return domain.Bill{}, domain.ErrNotFound
	}
	return b, nil
}

func (t *fakeTx) InsertBill(_ context.Context, b domain.Bill) error { t.putBill(b); return nil }
func (t *fakeTx) UpdateBill(_ context.Context, b domain.Bill) error { t.putBill(b); return nil }
func (t *fakeTx) putBill(b domain.Bill) {
	if t.billWrites == nil {
		t.billWrites = map[string]domain.Bill{}
	}
	t.billWrites[b.ID] = b
}

func (t *fakeTx) GetTab(_ context.Context, table int32) (domain.Tab, bool, error) {
	if t.tabWrites != nil {
		if tab, ok := t.tabWrites[table]; ok {
			return tab, true, nil
		}
	}
	tab, ok := t.repo.tabBucket(t.rid)[table]
	if !ok {
		return domain.Tab{}, false, nil
	}
	return tab, true, nil
}

func (t *fakeTx) UpsertTab(_ context.Context, tab domain.Tab) error {
	if t.tabWrites == nil {
		t.tabWrites = map[int32]domain.Tab{}
	}
	t.tabWrites[tab.Table] = tab
	return nil
}

func (t *fakeTx) DeleteTab(_ context.Context, table int32) error {
	if t.tabDeletes == nil {
		t.tabDeletes = map[int32]struct{}{}
	}
	t.tabDeletes[table] = struct{}{}
	return nil
}

func (t *fakeTx) Seen(_ context.Context, _ string, eventID string) (bool, error) {
	if eventID == "" {
		return false, nil
	}
	_, ok := t.repo.processed[t.rid+"|"+eventID]
	if ok {
		return true, nil
	}
	if t.marked != nil {
		_, ok = t.marked[t.rid+"|"+eventID]
	}
	return ok, nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, rid string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, RestaurantID: rid, Data: data})
	return nil
}

func (t *fakeTx) MarkProcessed(_ context.Context, rid, eventID string) error {
	if eventID == "" {
		return nil
	}
	if t.marked == nil {
		t.marked = map[string]struct{}{}
	}
	t.marked[rid+"|"+eventID] = struct{}{}
	return nil
}

// --- fake outbound clients ---

// fakeOrders implements ports.Orders. orders maps table -> orders; billed records
// the order ids passed to MarkBilled.
type fakeOrders struct {
	orders map[string][]ports.Order
	billed []string
}

func newFakeOrders() *fakeOrders { return &fakeOrders{orders: map[string][]ports.Order{}} }

func (o *fakeOrders) ListForTable(_ context.Context, _ string, table string) ([]ports.Order, error) {
	return o.orders[table], nil
}
func (o *fakeOrders) MarkBilled(_ context.Context, _ string, ids []string) error {
	o.billed = append(o.billed, ids...)
	return nil
}

// fakeMenu implements ports.Menu (itemID -> resolved name/category).
type fakeMenu struct {
	items map[string]ports.ResolvedItem
}

func newFakeMenu() *fakeMenu { return &fakeMenu{items: map[string]ports.ResolvedItem{}} }

func (m *fakeMenu) GetItem(_ context.Context, _ string, itemID string) (ports.ResolvedItem, error) {
	r, ok := m.items[itemID]
	if !ok {
		return ports.ResolvedItem{}, domain.ErrNotFound
	}
	return r, nil
}

// fakeSettings implements ports.Settings (fixed config).
type fakeSettings struct {
	cfg domain.TaxConfig
}

func newFakeSettings(gst, svc float64, rounding domain.Rounding) *fakeSettings {
	return &fakeSettings{cfg: domain.TaxConfig{GSTPct: gst, ServiceChargePct: svc, Rounding: rounding, Currency: "INR"}}
}

func (s *fakeSettings) BillingConfig(_ context.Context, _ string) (domain.TaxConfig, error) {
	return s.cfg, nil
}

// fakePromos implements ports.Promotions (code -> discount minor).
type fakePromos struct {
	discounts map[string]int64
}

func newFakePromos() *fakePromos { return &fakePromos{discounts: map[string]int64{}} }

func (p *fakePromos) Evaluate(_ context.Context, _ string, code string, _ money.Money) (int64, string, error) {
	return p.discounts[code], code, nil
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
