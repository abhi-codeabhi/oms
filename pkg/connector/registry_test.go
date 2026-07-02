package connector

import (
	"context"
	"testing"
)

type fakeConnector struct {
	id   string
	caps []Capability
}

func (f fakeConnector) Manifest() Manifest {
	return Manifest{ID: f.id, Name: f.id, Capabilities: f.caps}
}
func (f fakeConnector) Init(context.Context, map[string]string) error { return nil }

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	razorpay := fakeConnector{id: "razorpay", caps: []Capability{CapabilityPayment}}
	zomato := fakeConnector{id: "zomato", caps: []Capability{CapabilityAggregator}}
	twilio := fakeConnector{id: "twilio", caps: []Capability{CapabilityNotification, CapabilityPayment}}

	r.Register(razorpay)
	r.Register(zomato)
	r.Register(twilio)

	if got, ok := r.Get("razorpay"); !ok || got.Manifest().ID != "razorpay" {
		t.Fatalf("Get(razorpay) = %v, %v", got, ok)
	}
	if _, ok := r.Get("missing"); ok {
		t.Fatal("Get(missing) returned ok=true")
	}

	pay := r.ByCapability(CapabilityPayment)
	if len(pay) != 2 {
		t.Fatalf("ByCapability(payment) returned %d, want 2", len(pay))
	}
	agg := r.ByCapability(CapabilityAggregator)
	if len(agg) != 1 || agg[0].Manifest().ID != "zomato" {
		t.Fatalf("ByCapability(aggregator) = %v, want [zomato]", agg)
	}
	if len(r.ByCapability(CapabilityCRM)) != 0 {
		t.Fatal("ByCapability(crm) should be empty")
	}
}

func TestRegisterNilNoPanic(t *testing.T) {
	r := NewRegistry()
	r.Register(nil)
	if len(r.ByCapability(CapabilityPayment)) != 0 {
		t.Fatal("nil register polluted registry")
	}
}
