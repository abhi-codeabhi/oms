// Package nats provides a NATS JetStream implementation of the outbox.Bus
// publisher plus an idempotent durable subscriber. Subjects mirror the event
// type; consumers dedupe on Event.ID for exactly-once effect.
package nats

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/nats-io/nats.go"
	"github.com/restorna/platform/pkg/events"
	"github.com/restorna/platform/pkg/outbox"
)

// busImpl publishes events to JetStream; it satisfies outbox.Bus.
type busImpl struct {
	nc *nats.Conn
	js nats.JetStreamContext
}

// Connect dials url and returns a JetStream-backed Bus implementing outbox.Bus.
func Connect(url string) (outbox.Bus, error) {
	nc, err := nats.Connect(url, nats.Name("restorna"))
	if err != nil {
		return nil, fmt.Errorf("nats: connect: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("nats: jetstream: %w", err)
	}
	return &busImpl{nc: nc, js: js}, nil
}

// Publish marshals e and publishes it to a subject derived from its type, using
// the event id as the JetStream msg id so the server dedupes duplicate sends.
func (b *busImpl) Publish(ctx context.Context, e events.Event) error {
	payload, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("nats: marshal: %w", err)
	}
	subject := subjectFor(e.Type)
	_, err = b.js.Publish(subject, payload, nats.MsgId(e.ID), nats.Context(ctx))
	if err != nil {
		return fmt.Errorf("nats: publish %s: %w", subject, err)
	}
	return nil
}

// Subscribe creates a durable JetStream consumer on subject and invokes h for
// each event. Delivery is idempotent: an in-process seen-set dedupes on
// Event.ID so a redelivery never runs h twice. Messages are acked only when h
// returns nil; on error the message is nak'd for redelivery. Blocks until ctx
// is done.
func Subscribe(ctx context.Context, url, subject, durable string, h func(events.Event) error) error {
	nc, err := nats.Connect(url, nats.Name("restorna-sub-"+durable))
	if err != nil {
		return fmt.Errorf("nats: connect: %w", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		return fmt.Errorf("nats: jetstream: %w", err)
	}

	var mu sync.Mutex
	seen := make(map[string]struct{})

	sub, err := js.Subscribe(subject, func(msg *nats.Msg) {
		var e events.Event
		if err := json.Unmarshal(msg.Data, &e); err != nil {
			// Poison message: ack to drop it rather than loop forever.
			_ = msg.Ack()
			return
		}
		mu.Lock()
		_, dup := seen[e.ID]
		mu.Unlock()
		if dup {
			_ = msg.Ack()
			return
		}
		if err := h(e); err != nil {
			_ = msg.Nak()
			return
		}
		mu.Lock()
		seen[e.ID] = struct{}{}
		mu.Unlock()
		_ = msg.Ack()
	}, nats.Durable(durable), nats.ManualAck())
	if err != nil {
		return fmt.Errorf("nats: subscribe %s: %w", subject, err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	<-ctx.Done()
	return ctx.Err()
}

// subjectFor turns an event type into a NATS subject. Event types are already
// dotted (restorna.<context>.<aggregate>.<event>.v1), which maps cleanly.
func subjectFor(typ string) string {
	if typ == "" {
		return "restorna.unknown"
	}
	return typ
}
