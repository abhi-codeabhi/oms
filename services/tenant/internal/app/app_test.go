package app_test

import (
	"context"
	"errors"
	"testing"
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/tenant/internal/app"
	"github.com/restorna/platform/services/tenant/internal/domain"
)

func fixedClock() app.Now {
	t := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	return func() time.Time { return t }
}

// seedOwner inserts an owner directly via the repo so brand/outlet tests have a parent.
func seedOwner(t *testing.T, repo *fakeRepo) domain.Owner {
	t.Helper()
	o, err := domain.NewOwner("Acme Foods", "Acme Foods Pvt Ltd", "IN", time.Now())
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	repo.owners[o.ID] = o
	return o
}

func TestCreateOwner(t *testing.T) {
	tests := []struct {
		name    string
		in      app.CreateOwnerInput
		wantErr error
	}{
		{"ok", app.CreateOwnerInput{Name: "Acme", Country: "IN"}, nil},
		{"missing name", app.CreateOwnerInput{Country: "IN"}, domain.ErrInvalid},
		{"bad country", app.CreateOwnerInput{Name: "Acme", Country: "India"}, domain.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := newFakeRepo()
			a := app.New(repo, newFakeEntitlements(), &fakeBlob{}, fixedClock())
			o, err := a.CreateOwner(context.Background(), tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if !ids.Valid(domain.PrefixOwner, o.ID) {
				t.Fatalf("owner id %q not a valid own_ ULID", o.ID)
			}
			if _, ok := repo.owners[o.ID]; !ok {
				t.Fatal("owner not persisted")
			}
		})
	}
}

func TestCreateBrand_ReservesQuotaAndEmitsEvent(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	b, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Burger Co", PrimaryColor: "#FF8800"})
	if err != nil {
		t.Fatalf("create brand: %v", err)
	}
	if !ids.Valid(domain.PrefixBrand, b.ID) {
		t.Fatalf("brand id %q invalid", b.ID)
	}

	// quota reservation must have happened for key "brands", delta 1, idempotent by brand id.
	if len(ents.reserveCalls) != 1 {
		t.Fatalf("want 1 reserve call, got %d", len(ents.reserveCalls))
	}
	rc := ents.reserveCalls[0]
	if rc.Key != domain.QuotaBrands || rc.Delta != 1 || rc.ReservationID != b.ID {
		t.Fatalf("unexpected reserve call: %+v", rc)
	}

	// brand.created event must be staged.
	if got := countEvents(repo, app.EventBrandCreated); got != 1 {
		t.Fatalf("want 1 brand.created event, got %d", got)
	}
}

func TestCreateBrand_BlocksOverLimit(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaBrands] = 1 // only one brand allowed, no multi_brand feature
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	if _, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Brand 1"}); err != nil {
		t.Fatalf("first brand should succeed: %v", err)
	}
	_, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Brand 2"})
	if !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("second brand err = %v, want ErrQuotaExceeded", err)
	}
	var qe *app.QuotaError
	if !errors.As(err, &qe) || qe.Hint == "" {
		t.Fatalf("want QuotaError with upgrade hint, got %v", err)
	}
	// only one brand persisted.
	if len(repo.brands) != 1 {
		t.Fatalf("want 1 brand persisted, got %d", len(repo.brands))
	}
}

func TestCreateBrand_MultiBrandFeatureBypassesNumericQuota(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaBrands] = 1
	ents.features[domain.FeatureMultiBrand] = true // unlimited via feature gate
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	for i := 0; i < 3; i++ {
		if _, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "B"}); err != nil {
			t.Fatalf("brand %d should succeed via multi_brand: %v", i, err)
		}
	}
	if len(repo.brands) != 3 {
		t.Fatalf("want 3 brands, got %d", len(repo.brands))
	}
}

func TestCreateBrand_ReleasesQuotaOnPersistFailure(t *testing.T) {
	repo := newFakeRepo()
	repo.failInsert = true
	ents := newFakeEntitlements()
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	_, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Boom"})
	if err == nil {
		t.Fatal("expected persist failure")
	}
	if len(ents.releaseCalls) != 1 {
		t.Fatalf("want quota released on failure, got %d release calls", len(ents.releaseCalls))
	}
}

func TestCreateRestaurant_ReservesOutletsAndEmitsEvent(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaOutlets] = 5
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	brand, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Curry House"})
	if err != nil {
		t.Fatalf("create brand: %v", err)
	}

	r, err := a.CreateRestaurant(context.Background(), app.CreateRestaurantInput{
		OwnerID: owner.ID, BrandID: brand.ID, Name: "MG Road", Timezone: "Asia/Kolkata",
	})
	if err != nil {
		t.Fatalf("create restaurant: %v", err)
	}
	if !ids.Valid(domain.PrefixRestaurant, r.ID) {
		t.Fatalf("restaurant id %q invalid", r.ID)
	}
	if r.OwnerID != owner.ID || r.BrandID != brand.ID {
		t.Fatalf("hierarchy mismatch: %+v", r)
	}
	if !r.Active {
		t.Fatal("new outlet should be active")
	}

	// outlets quota reserved (key "outlets", delta 1, reservation == outlet id).
	var found bool
	for _, c := range ents.reserveCalls {
		if c.Key == domain.QuotaOutlets && c.Delta == 1 && c.ReservationID == r.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("outlets quota not reserved; calls=%+v", ents.reserveCalls)
	}
	if got := countEvents(repo, app.EventOutletProvisioned); got != 1 {
		t.Fatalf("want 1 outlet.provisioned event, got %d", got)
	}
}

func TestCreateRestaurant_BlocksOverOutletLimit(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaOutlets] = 1
	ents.limits[domain.QuotaBrands] = 5
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())

	brand, _ := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "B"})
	if _, err := a.CreateRestaurant(context.Background(), app.CreateRestaurantInput{OwnerID: owner.ID, BrandID: brand.ID, Name: "One"}); err != nil {
		t.Fatalf("first outlet should succeed: %v", err)
	}
	_, err := a.CreateRestaurant(context.Background(), app.CreateRestaurantInput{OwnerID: owner.ID, BrandID: brand.ID, Name: "Two"})
	if !errors.Is(err, domain.ErrQuotaExceeded) {
		t.Fatalf("second outlet err = %v, want ErrQuotaExceeded", err)
	}
	if len(repo.restaurants) != 1 {
		t.Fatalf("want 1 outlet persisted, got %d", len(repo.restaurants))
	}
}

func TestCreateRestaurant_UnknownBrand(t *testing.T) {
	repo := newFakeRepo()
	owner := seedOwner(t, repo)
	a := app.New(repo, newFakeEntitlements(), &fakeBlob{}, fixedClock())
	_, err := a.CreateRestaurant(context.Background(), app.CreateRestaurantInput{
		OwnerID: owner.ID, BrandID: ids.New(domain.PrefixBrand), Name: "Ghost",
	})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSetBrandLogo_StoresViaBlobAndSetsAsset(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	blob := &fakeBlob{}
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, blob, fixedClock())

	brand, _ := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Logo Co"})

	// upload via bytes -> blob store
	out, err := a.SetBrandLogo(context.Background(), owner.ID, brand.ID, []byte("PNGDATA"), "image/png", nil)
	if err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if blob.puts != 1 {
		t.Fatalf("want 1 blob put, got %d", blob.puts)
	}
	if out.Logo == nil || out.Logo.URL == "" {
		t.Fatalf("logo not set: %+v", out.Logo)
	}
	if repo.brands[brand.ID].Logo == nil {
		t.Fatal("logo not persisted on brand")
	}
}

func TestSetBrandLogo_PreUploadedAsset(t *testing.T) {
	repo := newFakeRepo()
	blob := &fakeBlob{}
	owner := seedOwner(t, repo)
	a := app.New(repo, newFakeEntitlements(), blob, fixedClock())
	brand, _ := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "Pre"})

	asset := &domain.Asset{ID: "ast_x", URL: "https://cdn/x.png", ContentType: "image/png"}
	out, err := a.SetBrandLogo(context.Background(), owner.ID, brand.ID, nil, "", asset)
	if err != nil {
		t.Fatalf("set logo: %v", err)
	}
	if blob.puts != 0 {
		t.Fatalf("should not call blob store for pre-uploaded asset, puts=%d", blob.puts)
	}
	if out.Logo == nil || out.Logo.URL != asset.URL {
		t.Fatalf("asset not attached: %+v", out.Logo)
	}
}

func TestSetRestaurantActive(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaOutlets] = 5
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())
	brand, _ := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "B"})
	r, _ := a.CreateRestaurant(context.Background(), app.CreateRestaurantInput{OwnerID: owner.ID, BrandID: brand.ID, Name: "X"})

	got, err := a.SetRestaurantActive(context.Background(), owner.ID, r.ID, false)
	if err != nil {
		t.Fatalf("set active: %v", err)
	}
	if got.Active {
		t.Fatal("expected inactive")
	}
	if repo.restaurants[r.ID].Active {
		t.Fatal("not persisted")
	}
}

func TestListBrands(t *testing.T) {
	repo := newFakeRepo()
	ents := newFakeEntitlements()
	ents.limits[domain.QuotaBrands] = -1
	owner := seedOwner(t, repo)
	a := app.New(repo, ents, &fakeBlob{}, fixedClock())
	for i := 0; i < 3; i++ {
		if _, err := a.CreateBrand(context.Background(), app.CreateBrandInput{OwnerID: owner.ID, Name: "B"}); err != nil {
			t.Fatalf("seed brand: %v", err)
		}
	}
	brands, total, err := a.ListBrands(context.Background(), owner.ID, 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(brands) != 3 {
		t.Fatalf("want 3 brands, got len=%d total=%d", len(brands), total)
	}
}

func TestListOwners_RequiresPlatformAdmin(t *testing.T) {
	repo := newFakeRepo()
	seedOwner(t, repo)
	a := app.New(repo, newFakeEntitlements(), &fakeBlob{}, fixedClock())

	tests := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{"no scope", context.Background(), true},
		{"owner role denied", tenancy.With(context.Background(), tenancy.Scope{Role: commonv1.Role_ROLE_OWNER}), true},
		{"platform admin allowed", tenancy.With(context.Background(), tenancy.Scope{Role: commonv1.Role_ROLE_PLATFORM_ADMIN}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := a.ListOwners(tc.ctx, "", 50, 0)
			if tc.wantErr {
				if !errors.Is(err, tenancy.ErrPermissionDenied) {
					t.Fatalf("err = %v, want ErrPermissionDenied", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
		})
	}
}

func TestListOwners_PaginatesAndFilters(t *testing.T) {
	repo := newFakeRepo()
	for _, name := range []string{"Acme Foods", "Acme Diner", "Bella Pizza"} {
		o, err := domain.NewOwner(name, "", "IN", time.Now())
		if err != nil {
			t.Fatalf("seed owner %q: %v", name, err)
		}
		repo.owners[o.ID] = o
	}
	a := app.New(repo, newFakeEntitlements(), &fakeBlob{}, fixedClock())
	ctx := tenancy.With(context.Background(), tenancy.Scope{Role: commonv1.Role_ROLE_PLATFORM_ADMIN})

	owners, total, err := a.ListOwners(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 || len(owners) != 3 {
		t.Fatalf("want 3 owners, got len=%d total=%d", len(owners), total)
	}

	// name filter (case-insensitive substring).
	owners, total, err = a.ListOwners(ctx, "acme", 50, 0)
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	if total != 2 || len(owners) != 2 {
		t.Fatalf("want 2 filtered owners, got len=%d total=%d", len(owners), total)
	}
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
