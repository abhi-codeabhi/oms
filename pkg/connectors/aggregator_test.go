package connectors

import (
	"context"
	"encoding/json"
	"testing"
)

func TestMockAgg_PushMenuCounts(t *testing.T) {
	m := NewMockAgg()
	menu := []byte(`{"items":[{"name":"A"},{"name":"B"},{"name":"C"}]}`)
	n, err := m.PushMenu(context.Background(), menu)
	if err != nil {
		t.Fatalf("PushMenu: %v", err)
	}
	if n != 3 {
		t.Fatalf("want 3 items, got %d", n)
	}
}

func TestMockAgg_VerifyWebhookNormalizes(t *testing.T) {
	m := NewMockAgg()
	body := []byte(`{
      "order_id": "ZOM-9001",
      "status": "received",
      "currency": "INR",
      "restaurant_id": "out_1",
      "placed_at": "2026-07-02T10:00:00Z",
      "items": [
        {"name": "Paneer Tikka", "qty": 2, "price_minor": 24000, "currency": "INR"},
        {"name": "Naan", "quantity": 3, "price_minor": 4000}
      ]
    }`)
	ev, err := m.VerifyWebhook(context.Background(), body, nil)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if ev.Type != EventAggregatorOrderReceived {
		t.Fatalf("event type = %q", ev.Type)
	}
	var d aggregatorOrderData
	if err := json.Unmarshal(ev.Data, &d); err != nil {
		t.Fatalf("decode data: %v", err)
	}
	if d.ConnectorID != "mockagg" || d.ExternalRef != "ZOM-9001" {
		t.Fatalf("unexpected normalized order: %+v", d)
	}
	if len(d.Items) != 2 || d.Items[0].Qty != 2 || d.Items[1].Qty != 3 {
		t.Fatalf("items not normalized (qty/quantity): %+v", d.Items)
	}
	if d.Items[1].Currency != "INR" { // inherited from top-level currency
		t.Fatalf("currency should fall back to order currency: %+v", d.Items[1])
	}
}

func TestNormalizeAggregatorOrder_MissingRef(t *testing.T) {
	if _, err := normalizeAggregatorOrder("zomato", []byte(`{"status":"received"}`)); err == nil {
		t.Fatal("want error when external ref missing")
	}
}

func TestZomato_VerifyWebhookSignature(t *testing.T) {
	z := NewZomato()
	if err := z.Init(context.Background(), map[string]string{
		"api_key": "k", "restaurant_id": "r", "webhook_secret": "shh",
	}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	body := []byte(`{"order_id":"Z-1","items":[{"name":"X","qty":1,"price_minor":100}]}`)
	good := hmacSHA256Hex("shh", body)
	if _, err := z.VerifyWebhook(context.Background(), body, map[string]string{"X-Zomato-Signature": good}); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	if _, err := z.VerifyWebhook(context.Background(), body, map[string]string{"X-Zomato-Signature": "deadbeef"}); err == nil {
		t.Fatal("bad signature accepted")
	}
}
