package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/services/tenant/internal/domain"
)

var now = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

func TestNewOwner(t *testing.T) {
	tests := []struct {
		name, owner, legal, country string
		wantErr                     bool
		wantLegal                   string
	}{
		{"ok", "Acme", "Acme Pvt Ltd", "in", false, "Acme Pvt Ltd"},
		{"legal defaults to name", "Acme", "", "IN", false, "Acme"},
		{"missing name", "", "x", "IN", true, ""},
		{"bad country", "Acme", "x", "IND", true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o, err := domain.NewOwner(tc.owner, tc.legal, tc.country, now)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixOwner, o.ID) {
				t.Fatalf("bad id %q", o.ID)
			}
			if o.LegalName != tc.wantLegal {
				t.Fatalf("legal = %q, want %q", o.LegalName, tc.wantLegal)
			}
			if o.Country != "IN" {
				t.Fatalf("country normalize = %q", o.Country)
			}
		})
	}
}

func TestNewBrand(t *testing.T) {
	owner := mustOwner(t)
	tests := []struct {
		name, ownerID, brand, color string
		wantErr                     bool
	}{
		{"ok no color", owner.ID, "Burger Co", "", false},
		{"ok hex color", owner.ID, "Burger Co", "#FF8800", false},
		{"ok short hex", owner.ID, "Burger Co", "#F80", false},
		{"bad owner id", "nope", "Burger Co", "", true},
		{"missing name", owner.ID, "", "", true},
		{"bad color", owner.ID, "Burger Co", "orange", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, err := domain.NewBrand(tc.ownerID, tc.brand, tc.color, now)
			if tc.wantErr {
				if !errors.Is(err, domain.ErrInvalid) {
					t.Fatalf("err = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixBrand, b.ID) {
				t.Fatalf("bad brand id %q", b.ID)
			}
			if b.OwnerID != owner.ID {
				t.Fatalf("owner mismatch")
			}
		})
	}
}

func TestNewRestaurant(t *testing.T) {
	owner := mustOwner(t)
	brand, _ := domain.NewBrand(owner.ID, "B", "", now)

	r, err := domain.NewRestaurant(brand.ID, owner.ID, "MG Road", "12 MG Rd", "", "29ABCDE1234F1Z5", now)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ids.Valid(domain.PrefixRestaurant, r.ID) {
		t.Fatalf("bad outlet id %q", r.ID)
	}
	if r.Timezone != "Asia/Kolkata" {
		t.Fatalf("timezone default = %q", r.Timezone)
	}
	if !r.Active {
		t.Fatal("new outlet should default active")
	}

	if _, err := domain.NewRestaurant("bad", owner.ID, "X", "", "", "", now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad brand id should fail: %v", err)
	}
	if _, err := domain.NewRestaurant(brand.ID, owner.ID, "", "", "", "", now); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("missing name should fail: %v", err)
	}
}

func TestBrandSetLogo(t *testing.T) {
	b, _ := domain.NewBrand(mustOwner(t).ID, "B", "", now)
	if err := b.SetLogo(domain.Asset{URL: ""}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("empty url should fail: %v", err)
	}
	if err := b.SetLogo(domain.Asset{ID: "ast_1", URL: "https://cdn/x.png", ContentType: "image/png"}); err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if b.Logo == nil || b.Logo.URL != "https://cdn/x.png" {
		t.Fatalf("logo not set: %+v", b.Logo)
	}
}

func TestRestaurantSetActive(t *testing.T) {
	owner := mustOwner(t)
	brand, _ := domain.NewBrand(owner.ID, "B", "", now)
	r, _ := domain.NewRestaurant(brand.ID, owner.ID, "X", "", "", "", now)
	r.SetActive(false)
	if r.Active {
		t.Fatal("expected inactive")
	}
	r.SetActive(true)
	if !r.Active {
		t.Fatal("expected active")
	}
}

func mustOwner(t *testing.T) domain.Owner {
	t.Helper()
	o, err := domain.NewOwner("Acme", "Acme", "IN", now)
	if err != nil {
		t.Fatalf("owner: %v", err)
	}
	return o
}
