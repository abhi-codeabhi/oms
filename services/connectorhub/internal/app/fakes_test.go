package app_test

import (
	"context"
	"errors"
	"sync"

	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/services/connectorhub/internal/domain"
	"github.com/restorna/platform/services/connectorhub/internal/ports"
)

// --- fakeRepo: in-memory ports.Repository (no DB) ---

type fakeRepo struct {
	mu         sync.Mutex
	byID       map[string]domain.Installation
	events     []stagedEvent
	failInsert bool // when true InsertInstallation errors (tests compensation)
}

type stagedEvent struct {
	Type    string
	OwnerID string
	Data    any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{byID: map[string]domain.Installation{}}
}

func (r *fakeRepo) Atomic(_ context.Context, _ string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r}
	if err := fn(tx); err != nil {
		return err
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) GetInstallation(_ context.Context, _, id string) (domain.Installation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byID[id]
	if !ok {
		return domain.Installation{}, domain.ErrNotFound
	}
	return i, nil
}

func (r *fakeRepo) ListInstallations(_ context.Context, ownerID string) ([]domain.Installation, error) {
	return r.list(ownerID), nil
}

func (r *fakeRepo) ListByOwner(_ context.Context, ownerID string) ([]domain.Installation, error) {
	return r.list(ownerID), nil
}

func (r *fakeRepo) list(ownerID string) []domain.Installation {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Installation
	for _, i := range r.byID {
		if i.OwnerID == ownerID {
			out = append(out, i)
		}
	}
	return out
}

type fakeTx struct {
	repo   *fakeRepo
	staged []stagedEvent
}

func (t *fakeTx) InsertInstallation(_ context.Context, i domain.Installation) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.repo.byID[i.ID] = i
	return nil
}

func (t *fakeTx) UpdateInstallation(_ context.Context, i domain.Installation) error {
	if _, ok := t.repo.byID[i.ID]; !ok {
		return domain.ErrNotFound
	}
	t.repo.byID[i.ID] = i
	return nil
}

func (t *fakeTx) GetInstallation(_ context.Context, id string) (domain.Installation, error) {
	i, ok := t.repo.byID[id]
	if !ok {
		return domain.Installation{}, domain.ErrNotFound
	}
	return i, nil
}

func (t *fakeTx) ExistsForConnector(_ context.Context, ownerID, restaurantID, connectorID string) (bool, error) {
	for _, i := range t.repo.byID {
		if i.OwnerID == ownerID && i.ConnectorID == connectorID && i.RestaurantID == restaurantID {
			return true, nil
		}
	}
	return false, nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, ownerID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, OwnerID: ownerID, Data: data})
	return nil
}

// --- fakeEntitlements: quota + feature flags ---

type fakeEntitlements struct {
	mu           sync.Mutex
	limits       map[string]int64 // key -> allowed (-1 unlimited)
	used         map[string]int64
	features     map[string]bool
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
		hint:     "Upgrade to Growth for more connectors.",
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
		limit = 1 // default: first install ok, second blocked
	}
	if limit == -1 || f.used[key]+delta <= limit {
		f.used[key] += delta
		rem := int64(-1)
		if limit != -1 {
			rem = limit - f.used[key]
		}
		return ports.ReserveResult{OK: true, Remaining: rem}, nil
	}
	return ports.ReserveResult{OK: false, UpgradeHint: f.hint}, nil
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

// --- fakeCrypto: reversible, tamper-detecting (real AES via domain.Envelope) ---
//
// We use the actual domain.Envelope so encrypt/decrypt round-trips and tampering is
// caught exactly as in production.

func newFakeCrypto() *domain.Envelope {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	env, err := domain.NewEnvelope(key)
	if err != nil {
		panic(err)
	}
	return env
}

// --- fakeConnectors: in-memory ports.Connectors ---

type fakeConn struct {
	manifest ports.ConnectorManifest
	// verify controls VerifyWebhook: returns (event, verified, err) built from the
	// stored fields. When signature != expectedSig it simulates a tampered payload.
	eventType   string
	expectedSig string
}

type fakeConnectors struct {
	byID map[string]fakeConn
	// lastSig records the signature the app passed via headers on the last verify.
	lastHeaders map[string]string
}

func newFakeConnectors(conns ...fakeConn) *fakeConnectors {
	m := map[string]fakeConn{}
	for _, c := range conns {
		m[c.manifest.ID] = c
	}
	return &fakeConnectors{byID: m}
}

func (f *fakeConnectors) All() []ports.ConnectorManifest {
	out := make([]ports.ConnectorManifest, 0, len(f.byID))
	for _, c := range f.byID {
		out = append(out, c.manifest)
	}
	return out
}

func (f *fakeConnectors) Get(id string) (ports.ConnectorManifest, bool) {
	c, ok := f.byID[id]
	if !ok {
		return ports.ConnectorManifest{}, false
	}
	return c.manifest, true
}

func (f *fakeConnectors) VerifyWebhook(_ context.Context, connectorID string, _ map[string]string, body []byte, headers map[string]string) (ports.Webhook, error) {
	f.lastHeaders = headers
	c, ok := f.byID[connectorID]
	if !ok {
		return ports.Webhook{}, errors.New("unknown connector")
	}
	sig := headers["X-Signature"]
	if sig != c.expectedSig {
		// tampered / forged payload -> reject
		return ports.Webhook{Verified: false}, errors.New("bad signature")
	}
	ev := events.New(c.eventType, "", map[string]any{"body_len": len(body)})
	return ports.Webhook{Event: ev, Verified: true}, nil
}

// --- fakeBus: records published events ---

type fakeBus struct {
	mu        sync.Mutex
	published []events.Event
	err       error
}

func (b *fakeBus) Publish(_ context.Context, e events.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.err != nil {
		return b.err
	}
	b.published = append(b.published, e)
	return nil
}

// --- manifest builders for tests ---

func manifestWith(id string, secretKeys []string, caps ...domain.Capability) ports.ConnectorManifest {
	sk := map[string]bool{}
	for _, k := range secretKeys {
		sk[k] = true
	}
	return ports.ConnectorManifest{
		ID:           id,
		Name:         id,
		Capabilities: caps,
		SecretKeys:   sk,
	}
}
