package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/aggregators/internal/domain"
)

func fixed() time.Time { return time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC) }

func validInput() domain.NewExternalOrderInput {
	return domain.NewExternalOrderInput{
		RestaurantID: "out_1",
		ConnectorID:  "zomato",
		ExternalRef:  "ZOM-9001",
		Status:       "received",
		Items:        []domain.Item{{Name: "Paneer Tikka", Qty: 2, Price: money.New(24000, "INR")}},
		PlacedAt:     "2026-07-02T10:00:00Z",
	}
}

func TestNewExternalOrder_OK(t *testing.T) {
	o, err := domain.NewExternalOrder(validInput(), fixed())
	if err != nil {
		t.Fatalf("NewExternalOrder: %v", err)
	}
	if !ids.Valid(domain.PrefixExternalOrder, o.ID) {
		t.Fatalf("id %q invalid", o.ID)
	}
	if o.Status != domain.StatusReceived {
		t.Fatalf("status = %q", o.Status)
	}
	if o.SyntheticTable() != "AGG-ZOM-9001" {
		t.Fatalf("synthetic table = %q", o.SyntheticTable())
	}
}

func TestNewExternalOrder_DefaultsStatus(t *testing.T) {
	in := validInput()
	in.Status = ""
	o, err := domain.NewExternalOrder(in, fixed())
	if err != nil {
		t.Fatalf("NewExternalOrder: %v", err)
	}
	if o.Status != domain.StatusReceived {
		t.Fatalf("blank status should default to received, got %q", o.Status)
	}
}

func TestNewExternalOrder_Validation(t *testing.T) {
	cases := map[string]func(*domain.NewExternalOrderInput){
		"no restaurant": func(i *domain.NewExternalOrderInput) { i.RestaurantID = "" },
		"no connector":  func(i *domain.NewExternalOrderInput) { i.ConnectorID = "" },
		"no ref":        func(i *domain.NewExternalOrderInput) { i.ExternalRef = "" },
		"no items":      func(i *domain.NewExternalOrderInput) { i.Items = nil },
		"bad status":    func(i *domain.NewExternalOrderInput) { i.Status = "teleported" },
	}
	for name, mut := range cases {
		in := validInput()
		mut(&in)
		if _, err := domain.NewExternalOrder(in, fixed()); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%s: want ErrInvalid, got %v", name, err)
		}
	}
}

func TestSetStatus(t *testing.T) {
	o, _ := domain.NewExternalOrder(validInput(), fixed())
	if err := o.SetStatus("accepted"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	if o.Status != domain.StatusAccepted {
		t.Fatalf("status = %q", o.Status)
	}
	if err := o.SetStatus("nope"); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad status should be ErrInvalid, got %v", err)
	}
	if err := o.SetStatus(""); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty status should be ErrInvalid, got %v", err)
	}
}
