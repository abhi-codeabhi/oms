package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/payments/internal/domain"
	"github.com/restorna/platform/services/payments/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). It is
// partitioned by restaurantID so tenant isolation is observable in tests, plus
// side indexes on idempotency key + provider ref that the app relies on.
type fakeRepo struct {
	mu         sync.Mutex
	payments   map[string]domain.Payment // paymentID -> payment
	byIdem     map[string]string         // restaurantID|key -> paymentID
	byProvRef  map[string]string         // providerRef -> paymentID
	events     []stagedEvent
	processed  map[string]struct{} // eventID
	failInsert bool
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		payments:  map[string]domain.Payment{},
		byIdem:    map[string]string{},
		byProvRef: map[string]string{},
		processed: map[string]struct{}{},
	}
}

func (r *fakeRepo) Atomic(_ context.Context, restaurantID string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, restaurantID: restaurantID}
	if err := fn(tx); err != nil {
		return err // buffered writes discarded (rollback)
	}
	for id, p := range tx.writes {
		r.payments[id] = p
		if p.ProviderRef != "" {
			r.byProvRef[p.ProviderRef] = id
		}
	}
	for k, id := range tx.idem {
		r.byIdem[k] = id
	}
	for k := range tx.markProcessed {
		r.processed[k] = struct{}{}
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) Get(_ context.Context, _ string, paymentID string) (domain.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.payments[paymentID]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return p, nil
}

func (r *fakeRepo) FindByIdempotencyKey(_ context.Context, restaurantID, key string) (domain.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byIdem[restaurantID+"|"+key]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return r.payments[id], nil
}

func (r *fakeRepo) FindByProviderRef(_ context.Context, _ string, providerRef string) (domain.Payment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byProvRef[providerRef]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return r.payments[id], nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo          *fakeRepo
	restaurantID  string
	writes        map[string]domain.Payment
	idem          map[string]string
	staged        []stagedEvent
	markProcessed map[string]struct{}
}

func (t *fakeTx) Get(_ context.Context, paymentID string) (domain.Payment, error) {
	if t.writes != nil {
		if w, ok := t.writes[paymentID]; ok {
			return w, nil
		}
	}
	p, ok := t.repo.payments[paymentID]
	if !ok {
		return domain.Payment{}, domain.ErrNotFound
	}
	return p, nil
}

func (t *fakeTx) Insert(_ context.Context, p domain.Payment, idempotencyKey string) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.put(p)
	if t.idem == nil {
		t.idem = map[string]string{}
	}
	t.idem[p.RestaurantID+"|"+idempotencyKey] = p.ID
	return nil
}

func (t *fakeTx) Update(_ context.Context, p domain.Payment) error {
	if _, ok := t.repo.payments[p.ID]; !ok {
		return domain.ErrNotFound
	}
	t.put(p)
	return nil
}

func (t *fakeTx) put(p domain.Payment) {
	if t.writes == nil {
		t.writes = map[string]domain.Payment{}
	}
	t.writes[p.ID] = p
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, restaurantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, RestaurantID: restaurantID, Data: data})
	return nil
}

func (t *fakeTx) MarkProcessed(_ context.Context, _, eventID string) error {
	if eventID == "" {
		return nil
	}
	if t.markProcessed == nil {
		t.markProcessed = map[string]struct{}{}
	}
	t.markProcessed[eventID] = struct{}{}
	return nil
}

// fakeHub is an in-memory ports.ConnectorHub. It records Resolve calls and returns
// a configured connector id/config so tests can assert provider resolution.
type fakeHub struct {
	mu           sync.Mutex
	connectorID  string
	config       map[string]string
	resolveCalls []resolveCall
	err          error
}

type resolveCall struct{ RestaurantID, Prefer string }

func newFakeHub(connectorID string) *fakeHub {
	return &fakeHub{connectorID: connectorID, config: map[string]string{"key_id": "k_test"}}
}

func (h *fakeHub) ResolvePayment(_ context.Context, restaurantID, prefer string) (ports.Resolved, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.resolveCalls = append(h.resolveCalls, resolveCall{restaurantID, prefer})
	if h.err != nil {
		return ports.Resolved{}, h.err
	}
	return ports.Resolved{ConnectorID: h.connectorID, Config: h.config}, nil
}

// fakeFactory builds fakeProviders and records the connector ids requested.
type fakeFactory struct {
	mu       sync.Mutex
	built    []string
	provider *fakeProvider
	buildErr error
}

func newFakeFactory() *fakeFactory { return &fakeFactory{provider: &fakeProvider{}} }

func (f *fakeFactory) Payment(_ context.Context, connectorID string, _ map[string]string) (ports.PaymentProvider, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built = append(f.built, connectorID)
	if f.buildErr != nil {
		return nil, f.buildErr
	}
	return f.provider, nil
}

// fakeProvider is an in-memory ports.PaymentProvider. It mints a deterministic
// provider ref, records capture/refund calls, and can be forced to error.
type fakeProvider struct {
	mu           sync.Mutex
	nextRef      string
	createCalls  int
	captureCalls []string
	refundCalls  []money.Money
	createErr    error
	captureErr   error
	refundErr    error
}

func (p *fakeProvider) CreateIntent(_ context.Context, _ money.Money, _ string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.createCalls++
	if p.createErr != nil {
		return "", p.createErr
	}
	ref := p.nextRef
	if ref == "" {
		ref = "prov_ref_1"
	}
	return ref, nil
}

func (p *fakeProvider) Capture(_ context.Context, providerRef string) (json.RawMessage, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.captureCalls = append(p.captureCalls, providerRef)
	if p.captureErr != nil {
		return nil, p.captureErr
	}
	return json.RawMessage(`{"status":"captured"}`), nil
}

func (p *fakeProvider) Refund(_ context.Context, _ string, amount money.Money) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refundCalls = append(p.refundCalls, amount)
	return p.refundErr
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
