package app_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/services/aggregators/internal/app"
	"github.com/restorna/platform/services/aggregators/internal/domain"
	"github.com/restorna/platform/services/aggregators/internal/ports"
)

const rid = "out_01hx0000000000000000000000"

func fixedClock() app.Now {
	t := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

func sampleOrder() app.AggregatorOrder {
	return app.AggregatorOrder{
		EventID:      "evt_1",
		RestaurantID: rid,
		ConnectorID:  "zomato",
		ExternalRef:  "ZOM-9001",
		Status:       "received",
		PlacedAt:     "2026-07-02T10:00:00Z",
		Items: []app.AggregatorOrderItem{
			{Name: "Paneer Tikka", Qty: 2, PriceMinor: 24000, Currency: "INR"},
			{Name: "Naan", Qty: 3, PriceMinor: 4000, Currency: "INR"},
		},
	}
}

// --- PushMenu ---

func TestPushMenu_PullsCatalogAndPushesToAdapter(t *testing.T) {
	repo := newFakeRepo()
	cat := newFakeCatalog()
	cat.items[rid] = []ports.MenuItem{
		{ID: "item_1", Name: "Paneer Tikka", PriceMinor: 24000, Currency: "INR", Available: true, Station: "tandoor"},
		{ID: "item_2", Name: "Naan", PriceMinor: 4000, Currency: "INR", Available: true, Station: "tandoor"},
	}
	hub := newFakeHub()
	hub.accepted = 2
	a := app.New(repo, hub, cat, newFakeOrdering(), fixedClock())

	res, err := a.PushMenu(context.Background(), rid, "")
	if err != nil {
		t.Fatalf("PushMenu: %v", err)
	}
	if !res.OK || res.Items != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if hub.pushedRC.ConnectorID != "mockagg" {
		t.Fatalf("resolved connector = %q", hub.pushedRC.ConnectorID)
	}
	// The serialized menu must carry both catalog items.
	var w struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := json.Unmarshal(hub.pushed, &w); err != nil {
		t.Fatalf("decode pushed menu: %v", err)
	}
	if len(w.Items) != 2 {
		t.Fatalf("pushed menu should have 2 items, got %d", len(w.Items))
	}
}

func TestPushMenu_PrefersConnector(t *testing.T) {
	cat := newFakeCatalog()
	cat.items[rid] = []ports.MenuItem{{ID: "i", Name: "X", Available: true}}
	hub := newFakeHub()
	a := app.New(newFakeRepo(), hub, cat, newFakeOrdering(), fixedClock())
	if _, err := a.PushMenu(context.Background(), rid, "swiggy"); err != nil {
		t.Fatalf("PushMenu: %v", err)
	}
	if hub.pushedRC.ConnectorID != "swiggy" {
		t.Fatalf("prefer_connector_id ignored: %q", hub.pushedRC.ConnectorID)
	}
}

func TestPushMenu_Validation(t *testing.T) {
	a := app.New(newFakeRepo(), newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())
	if _, err := a.PushMenu(context.Background(), "", ""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty restaurant should be ErrInvalid, got %v", err)
	}
}

// --- OnAggregatorOrder (choreography) ---

func TestOnAggregatorOrder_PersistsAndForwards(t *testing.T) {
	repo := newFakeRepo()
	ord := newFakeOrdering()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), ord, fixedClock())

	if err := a.OnAggregatorOrder(context.Background(), sampleOrder()); err != nil {
		t.Fatalf("OnAggregatorOrder: %v", err)
	}

	// Persisted exactly one ExternalOrder.
	got, err := repo.List(context.Background(), rid, "", "")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 persisted order, got %d", len(got))
	}
	o := got[0]
	if o.ConnectorID != "zomato" || o.ExternalRef != "ZOM-9001" || o.Status != domain.StatusReceived {
		t.Fatalf("unexpected order: %+v", o)
	}

	// Forwarded to ordering at the synthetic table with both lines.
	if len(ord.placed) != 1 {
		t.Fatalf("want 1 forwarded order, got %d", len(ord.placed))
	}
	fwd := ord.placed[0]
	if fwd.Table != "AGG-ZOM-9001" || fwd.RestaurantID != rid {
		t.Fatalf("forwarded to wrong table/restaurant: %+v", fwd)
	}
	if len(fwd.Lines) != 2 || fwd.Lines[0].Qty != 2 || fwd.Lines[1].Qty != 3 {
		t.Fatalf("forwarded lines wrong: %+v", fwd.Lines)
	}

	// Emitted the order.received event.
	if n := countEvents(repo, app.EventOrderReceived); n != 1 {
		t.Fatalf("want 1 order.received event, got %d", n)
	}
}

func TestOnAggregatorOrder_IdempotentOnEventID(t *testing.T) {
	repo := newFakeRepo()
	ord := newFakeOrdering()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), ord, fixedClock())

	ev := sampleOrder()
	if err := a.OnAggregatorOrder(context.Background(), ev); err != nil {
		t.Fatalf("first delivery: %v", err)
	}
	// Redeliver the SAME event id -> no new order, no second forward.
	if err := a.OnAggregatorOrder(context.Background(), ev); err != nil {
		t.Fatalf("redelivery: %v", err)
	}
	got, _ := repo.List(context.Background(), rid, "", "")
	if len(got) != 1 {
		t.Fatalf("redelivery must not create a duplicate; got %d orders", len(got))
	}
	if len(ord.placed) != 1 {
		t.Fatalf("redelivery must not re-forward; got %d forwards", len(ord.placed))
	}
	if n := countEvents(repo, app.EventOrderReceived); n != 1 {
		t.Fatalf("redelivery must not re-emit; got %d events", n)
	}
}

func TestOnAggregatorOrder_IdempotentOnExternalRef(t *testing.T) {
	repo := newFakeRepo()
	ord := newFakeOrdering()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), ord, fixedClock())

	first := sampleOrder()
	if err := a.OnAggregatorOrder(context.Background(), first); err != nil {
		t.Fatalf("first: %v", err)
	}
	// Same aggregator order (connector+ref), but a DIFFERENT event id -> still deduped.
	second := sampleOrder()
	second.EventID = "evt_2"
	if err := a.OnAggregatorOrder(context.Background(), second); err != nil {
		t.Fatalf("second: %v", err)
	}
	got, _ := repo.List(context.Background(), rid, "", "")
	if len(got) != 1 {
		t.Fatalf("same connector+ref must dedupe across event ids; got %d", len(got))
	}
	if len(ord.placed) != 1 {
		t.Fatalf("must not re-forward the same aggregator order; got %d", len(ord.placed))
	}
}

func TestOnAggregatorOrder_Validation(t *testing.T) {
	a := app.New(newFakeRepo(), newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())
	cases := []app.AggregatorOrder{
		{EventID: "e", RestaurantID: "", ConnectorID: "zomato", ExternalRef: "r", Items: []app.AggregatorOrderItem{{Name: "x", Qty: 1}}},
		{EventID: "e", RestaurantID: rid, ConnectorID: "zomato", ExternalRef: "r", Items: nil},
	}
	for i, ev := range cases {
		if err := a.OnAggregatorOrder(context.Background(), ev); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("case %d: want ErrInvalid, got %v", i, err)
		}
	}
}

// --- AckExternalOrder ---

func TestAckExternalOrder_UpdatesStatus(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())
	if err := a.OnAggregatorOrder(context.Background(), sampleOrder()); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, _ := repo.List(context.Background(), rid, "", "")
	id := got[0].ID

	updated, err := a.AckExternalOrder(context.Background(), rid, id, "accepted")
	if err != nil {
		t.Fatalf("Ack: %v", err)
	}
	if updated.Status != domain.StatusAccepted {
		t.Fatalf("status = %q", updated.Status)
	}
	// Reject path.
	rejected, err := a.AckExternalOrder(context.Background(), rid, id, "rejected")
	if err != nil {
		t.Fatalf("Ack reject: %v", err)
	}
	if rejected.Status != domain.StatusRejected {
		t.Fatalf("status = %q", rejected.Status)
	}
}

func TestAckExternalOrder_NotFound(t *testing.T) {
	a := app.New(newFakeRepo(), newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())
	if _, err := a.AckExternalOrder(context.Background(), rid, "ext_missing", "accepted"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestAckExternalOrder_BadStatus(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())
	_ = a.OnAggregatorOrder(context.Background(), sampleOrder())
	got, _ := repo.List(context.Background(), rid, "", "")
	if _, err := a.AckExternalOrder(context.Background(), rid, got[0].ID, "levitating"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("want ErrInvalid, got %v", err)
	}
}

// --- ListExternalOrders ---

func TestListExternalOrders_Filters(t *testing.T) {
	repo := newFakeRepo()
	a := app.New(repo, newFakeHub(), newFakeCatalog(), newFakeOrdering(), fixedClock())

	z := sampleOrder()
	if err := a.OnAggregatorOrder(context.Background(), z); err != nil {
		t.Fatalf("seed zomato: %v", err)
	}
	s := sampleOrder()
	s.EventID = "evt_s"
	s.ConnectorID = "swiggy"
	s.ExternalRef = "SWG-1"
	if err := a.OnAggregatorOrder(context.Background(), s); err != nil {
		t.Fatalf("seed swiggy: %v", err)
	}

	all, err := a.ListExternalOrders(context.Background(), rid, "", "")
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 orders, got %d", len(all))
	}
	onlyZ, _ := a.ListExternalOrders(context.Background(), rid, "zomato", "")
	if len(onlyZ) != 1 || onlyZ[0].ConnectorID != "zomato" {
		t.Fatalf("connector filter failed: %+v", onlyZ)
	}
	received, _ := a.ListExternalOrders(context.Background(), rid, "", "received")
	if len(received) != 2 {
		t.Fatalf("status filter failed: %d", len(received))
	}
}
