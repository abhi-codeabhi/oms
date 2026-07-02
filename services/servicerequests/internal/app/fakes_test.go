package app_test

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/restorna/platform/services/servicerequests/internal/domain"
	"github.com/restorna/platform/services/servicerequests/internal/ports"
)

// fakeRepo is an in-memory ports.Repository for unit tests (no DB). It is
// partitioned by restaurantID so tenant isolation is observable in tests.
type fakeRepo struct {
	mu         sync.Mutex
	requests   map[string]map[string]domain.Request // restaurantID -> requestID -> request
	cooldowns  map[string]time.Time                 // restaurantID|table|type -> last ack
	events     []stagedEvent
	failInsert bool
}

type stagedEvent struct {
	Type         string
	RestaurantID string
	Data         any
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		requests:  map[string]map[string]domain.Request{},
		cooldowns: map[string]time.Time{},
	}
}

func cdKey(table int32, typ domain.Type) string {
	return string(typ) + "|" + itoa(table)
}

func itoa(n int32) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func (r *fakeRepo) bucket(restaurantID string) map[string]domain.Request {
	if r.requests[restaurantID] == nil {
		r.requests[restaurantID] = map[string]domain.Request{}
	}
	return r.requests[restaurantID]
}

func (r *fakeRepo) Atomic(_ context.Context, restaurantID string, fn func(ports.Tx) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	tx := &fakeTx{repo: r, restaurantID: restaurantID}
	if err := fn(tx); err != nil {
		return err // staged writes discarded (rollback)
	}
	for id, req := range tx.writes {
		r.bucket(restaurantID)[id] = req
	}
	for k, at := range tx.cooldownWrites {
		r.cooldowns[restaurantID+"|"+k] = at
	}
	r.events = append(r.events, tx.staged...)
	return nil
}

func (r *fakeRepo) List(_ context.Context, restaurantID string) ([]domain.Request, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Request
	for _, req := range r.bucket(restaurantID) {
		out = append(out, req)
	}
	return out, nil
}

func (r *fakeRepo) LastAck(_ context.Context, restaurantID string, table int32, typ domain.Type) (time.Time, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cooldowns[restaurantID+"|"+cdKey(table, typ)], nil
}

// fakeTx buffers writes so Atomic can commit/rollback atomically.
type fakeTx struct {
	repo           *fakeRepo
	restaurantID   string
	writes         map[string]domain.Request
	cooldownWrites map[string]time.Time
	staged         []stagedEvent
}

func (t *fakeTx) Get(_ context.Context, requestID string) (domain.Request, error) {
	if t.writes != nil {
		if w, ok := t.writes[requestID]; ok {
			return w, nil
		}
	}
	req, ok := t.repo.bucket(t.restaurantID)[requestID]
	if !ok {
		return domain.Request{}, domain.ErrNotFound
	}
	return req, nil
}

func (t *fakeTx) Insert(_ context.Context, r domain.Request) error {
	if t.repo.failInsert {
		return errors.New("insert failed")
	}
	t.put(r)
	return nil
}

func (t *fakeTx) Update(_ context.Context, r domain.Request) error {
	t.put(r)
	return nil
}

func (t *fakeTx) put(r domain.Request) {
	if t.writes == nil {
		t.writes = map[string]domain.Request{}
	}
	t.writes[r.ID] = r
}

func (t *fakeTx) SetLastAck(_ context.Context, _ string, table int32, typ domain.Type, at time.Time) error {
	if t.cooldownWrites == nil {
		t.cooldownWrites = map[string]time.Time{}
	}
	t.cooldownWrites[cdKey(table, typ)] = at
	return nil
}

func (t *fakeTx) StageEvent(_ context.Context, eventType, restaurantID string, data any) error {
	t.staged = append(t.staged, stagedEvent{Type: eventType, RestaurantID: restaurantID, Data: data})
	return nil
}

// fakeSettings returns fixed thresholds (or an error to exercise the default
// fallback path).
type fakeSettings struct {
	cooldown   time.Duration
	escalation time.Duration
	err        error
}

func (s fakeSettings) Thresholds(_ context.Context, _ string) (ports.Thresholds, error) {
	if s.err != nil {
		return ports.Thresholds{}, s.err
	}
	return ports.Thresholds{Cooldown: s.cooldown, Escalation: s.escalation}, nil
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
