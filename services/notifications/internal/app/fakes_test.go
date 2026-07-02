package app_test

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/restorna/platform/services/notifications/internal/domain"
	"github.com/restorna/platform/services/notifications/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). Templates are
// keyed by (owner,id); a "own_platform" owner holds default copy so the fallback
// path is exercised exactly as the real repo does.
type fakeRepo struct {
	mu        sync.Mutex
	messages  map[string]domain.Message
	templates map[string]domain.Template // key = owner + "|" + id
	processed map[string]bool
	events    []stagedEvent
	failWrite bool
}

type stagedEvent struct {
	Type    string
	OwnerID string
	Data    any
}

const platformOwner = "own_platform"

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		messages:  map[string]domain.Message{},
		templates: map[string]domain.Template{},
		processed: map[string]bool{},
	}
}

func tkey(owner, id string) string { return owner + "|" + id }

// seedTemplate registers a template under owner (use platformOwner for defaults).
func (r *fakeRepo) seedTemplate(t domain.Template) {
	r.templates[tkey(t.OwnerID, t.ID)] = t
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

func (r *fakeRepo) GetMessage(_ context.Context, _, id string) (domain.Message, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.messages[id]
	if !ok {
		return domain.Message{}, domain.ErrNotFound
	}
	return m, nil
}

func (r *fakeRepo) FindByIdempotencyKey(_ context.Context, ownerID, key string) (domain.Message, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.messages {
		if m.OwnerID == ownerID && m.IdempotencyKey == key && key != "" {
			return m, true, nil
		}
	}
	return domain.Message{}, false, nil
}

func (r *fakeRepo) FindByProviderRef(_ context.Context, providerID, providerRef string) (domain.Message, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, m := range r.messages {
		if m.ProviderID == providerID && m.ProviderRef == providerRef && providerRef != "" {
			return m, true, nil
		}
	}
	return domain.Message{}, false, nil
}

func (r *fakeRepo) GetTemplate(_ context.Context, ownerID, templateID string) (domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t, ok := r.templates[tkey(ownerID, templateID)]; ok {
		return t, nil
	}
	if t, ok := r.templates[tkey(platformOwner, templateID)]; ok {
		return t, nil
	}
	return domain.Template{}, domain.ErrNotFound
}

func (r *fakeRepo) ListTemplates(_ context.Context, ownerID string) ([]domain.Template, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Template
	for _, t := range r.templates {
		if t.OwnerID == ownerID {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *fakeRepo) UpsertTemplate(_ context.Context, t domain.Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.templates[tkey(t.OwnerID, t.ID)] = t
	return nil
}

func (r *fakeRepo) UpdateDeliveryStatus(ctx context.Context, m domain.Message) error {
	return r.Atomic(ctx, m.OwnerID, func(tx ports.Tx) error { return tx.UpdateMessage(ctx, m) })
}

func (r *fakeRepo) MarkEventProcessed(_ context.Context, eventID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.processed[eventID] {
		return false, nil
	}
	r.processed[eventID] = true
	return true, nil
}

// fakeTx is the unit-of-work over fakeRepo (already holds the repo lock via Atomic).
type fakeTx struct {
	repo   *fakeRepo
	staged []stagedEvent
}

func (t *fakeTx) InsertMessage(_ context.Context, m domain.Message) error {
	if t.repo.failWrite {
		return errors.New("insert failed")
	}
	t.repo.messages[m.ID] = m
	return nil
}

func (t *fakeTx) UpdateMessage(_ context.Context, m domain.Message) error {
	if _, ok := t.repo.messages[m.ID]; !ok {
		return domain.ErrNotFound
	}
	t.repo.messages[m.ID] = m
	return nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, ownerID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, OwnerID: ownerID, Data: data})
	return nil
}

// fakeHub is an in-memory ports.ConnectorHub. installed=false exercises the mock
// fallback; a set connectorID + config resolves a real provider.
type fakeHub struct {
	res       ports.Resolution
	err       error
	callCount int
}

func (h *fakeHub) Resolve(_ context.Context, _ string) (ports.Resolution, error) {
	h.callCount++
	if h.err != nil {
		return ports.Resolution{}, h.err
	}
	return h.res, nil
}

// fakeSender records the sends it receives and can be told to fail.
type fakeSender struct {
	id       string
	ref      string
	err      error
	sent     []sentMsg
	failNext bool
}

type sentMsg struct{ Channel, To, Subject, Body string }

func (s *fakeSender) Send(_ context.Context, channel, to, subject, body string) (string, error) {
	s.sent = append(s.sent, sentMsg{channel, to, subject, body})
	if s.failNext || s.err != nil {
		if s.err != nil {
			return "", s.err
		}
		return "", errors.New("provider send failed")
	}
	ref := s.ref
	if ref == "" {
		ref = "ref_" + s.id
	}
	return ref, nil
}

// fakeFactory implements ports.ProviderFactory. real is returned by New (unless
// newErr is set); fallback is the built-in mock returned by Fallback.
type fakeFactory struct {
	real       *fakeSender
	fallback   *fakeSender
	fallbackID string
	newErr     error
	newCalls   []string
}

func newFakeFactory() *fakeFactory {
	return &fakeFactory{
		real:       &fakeSender{id: "twilio", ref: "twref"},
		fallback:   &fakeSender{id: "lognotify", ref: "logref"},
		fallbackID: "lognotify",
	}
}

func (f *fakeFactory) New(_ context.Context, connectorID string, _ map[string]string) (ports.NotificationSender, error) {
	f.newCalls = append(f.newCalls, connectorID)
	if f.newErr != nil {
		return nil, f.newErr
	}
	f.real.id = connectorID
	return f.real, nil
}

func (f *fakeFactory) Fallback(_ context.Context) (ports.NotificationSender, string) {
	return f.fallback, f.fallbackID
}

func countEvents(repo *fakeRepo, typ string) int {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	n := 0
	for _, e := range repo.events {
		if e.Type == typ {
			n++
		}
	}
	return n
}
