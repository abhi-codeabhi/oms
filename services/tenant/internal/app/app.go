// Package app holds the tenant use cases. It depends only on ports + domain. It
// orchestrates quota reservation (entitlements), persistence (repo), branding
// (blob store) and event emission (outbox via Tx.StageEvent). The grpc adapter
// maps proto <-> these calls; tests drive it with in-memory fakes.
package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/restorna/platform/gen/go/restorna/common/v1"
	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/tenancy"
	"github.com/restorna/platform/services/tenant/internal/domain"
	"github.com/restorna/platform/services/tenant/internal/ports"
)

// Event types emitted by this service (see CONVENTIONS.md naming).
const (
	EventBrandCreated      = "restorna.tenant.brand.created.v1"
	EventOutletProvisioned = "restorna.tenant.outlet.provisioned.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo  ports.Repository
	ents  ports.Entitlements
	blobs ports.BlobStore
	now   Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, ents ports.Entitlements, blobs ports.BlobStore, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, ents: ents, blobs: blobs, now: now}
}

// --- Owner ---

// CreateOwnerInput is the validated input for creating an owner.
type CreateOwnerInput struct{ Name, LegalName, Country string }

// CreateOwner provisions a new billable owner account.
func (a *App) CreateOwner(ctx context.Context, in CreateOwnerInput) (domain.Owner, error) {
	o, err := domain.NewOwner(in.Name, in.LegalName, in.Country, a.now())
	if err != nil {
		return domain.Owner{}, err
	}
	// Owner creation is its own tenant root; scope the tx to the new owner id.
	err = a.repo.Atomic(ctx, o.ID, func(tx ports.Tx) error {
		return tx.InsertOwner(ctx, o)
	})
	if err != nil {
		return domain.Owner{}, err
	}
	return o, nil
}

// GetOwner returns the owner by id (RLS scopes to the caller's owner).
func (a *App) GetOwner(ctx context.Context, ownerID string) (domain.Owner, error) {
	if !ids.Valid(domain.PrefixOwner, ownerID) {
		return domain.Owner{}, fmt.Errorf("%w: owner_id is invalid", domain.ErrInvalid)
	}
	return a.repo.GetOwner(ctx, ownerID)
}

// ListOwners returns a cross-tenant, paginated index of all owners. It is
// platform-admin only: the caller's JWT-derived scope MUST carry
// ROLE_PLATFORM_ADMIN or it returns a permission-denied error. query optionally
// filters owners by name (case-insensitive substring).
func (a *App) ListOwners(ctx context.Context, query string, limit, offset int) ([]domain.Owner, int, error) {
	scope, ok := tenancy.From(ctx)
	if !ok {
		return nil, 0, tenancy.ErrPermissionDenied
	}
	if err := scope.Require(commonv1.Role_ROLE_PLATFORM_ADMIN); err != nil {
		return nil, 0, err
	}
	limit = clampLimit(limit)
	if offset < 0 {
		offset = 0
	}
	return a.repo.ListOwners(ctx, query, limit, offset)
}

// --- Brand ---

// CreateBrandInput is the validated input for creating a brand.
type CreateBrandInput struct{ OwnerID, Name, PrimaryColor string }

// CreateBrand reserves the "brands" quota (gated by the "multi_brand" feature for
// the 2nd+ brand) BEFORE persisting, then stages the brand.created event. On any
// failure after a successful reservation the quota is released (compensation).
func (a *App) CreateBrand(ctx context.Context, in CreateBrandInput) (domain.Brand, error) {
	b, err := domain.NewBrand(in.OwnerID, in.Name, in.PrimaryColor, a.now())
	if err != nil {
		return domain.Brand{}, err
	}

	// Quota gate: reserve one "brands" slot. The reservation id is the brand id so
	// the reservation is idempotent and traceable to the created resource.
	res, err := a.ents.ReserveQuota(ctx, in.OwnerID, domain.QuotaBrands, 1, b.ID)
	if err != nil {
		return domain.Brand{}, fmt.Errorf("reserve brands quota: %w", err)
	}
	if !res.OK {
		// Fall back to the multi_brand feature gate so plans can express "many
		// brands" either as a numeric quota or a boolean unlock.
		enabled, ferr := a.ents.HasFeature(ctx, in.OwnerID, domain.FeatureMultiBrand)
		if ferr == nil && enabled {
			// feature unlocks unlimited brands; proceed without a numeric slot.
		} else {
			return domain.Brand{}, quotaErr(domain.QuotaBrands, res.UpgradeHint)
		}
	}

	err = a.repo.Atomic(ctx, in.OwnerID, func(tx ports.Tx) error {
		if err := tx.InsertBrand(ctx, b); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventBrandCreated, b.OwnerID, brandEvent(b))
	})
	if err != nil {
		if res.OK {
			_ = a.ents.ReleaseQuota(ctx, in.OwnerID, domain.QuotaBrands, 1, b.ID)
		}
		return domain.Brand{}, err
	}
	return b, nil
}

// SetBrandLogo stores the logo bytes via the blob store and attaches the returned
// asset to the brand. If logoBytes is empty, asset is taken as a pre-uploaded ref.
func (a *App) SetBrandLogo(ctx context.Context, ownerID, brandID string, logoBytes []byte, contentType string, preUploaded *domain.Asset) (domain.Brand, error) {
	if !ids.Valid(domain.PrefixBrand, brandID) {
		return domain.Brand{}, fmt.Errorf("%w: brand_id is invalid", domain.ErrInvalid)
	}

	var asset domain.Asset
	switch {
	case len(logoBytes) > 0:
		stored, err := a.blobs.Put(ctx, logoBytes, contentType)
		if err != nil {
			return domain.Brand{}, fmt.Errorf("store logo: %w", err)
		}
		asset = stored
	case preUploaded != nil && preUploaded.URL != "":
		asset = *preUploaded
	default:
		return domain.Brand{}, fmt.Errorf("%w: logo bytes or asset reference required", domain.ErrInvalid)
	}

	var out domain.Brand
	err := a.repo.Atomic(ctx, ownerID, func(tx ports.Tx) error {
		b, err := tx.GetBrand(ctx, brandID)
		if err != nil {
			return err
		}
		if err := b.SetLogo(asset); err != nil {
			return err
		}
		if err := tx.UpdateBrand(ctx, b); err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return domain.Brand{}, err
	}
	return out, nil
}

// ListBrands returns the owner's brands, paginated.
func (a *App) ListBrands(ctx context.Context, ownerID string, limit, offset int) ([]domain.Brand, int, error) {
	if !ids.Valid(domain.PrefixOwner, ownerID) {
		return nil, 0, fmt.Errorf("%w: owner_id is invalid", domain.ErrInvalid)
	}
	limit = clampLimit(limit)
	return a.repo.ListBrands(ctx, ownerID, limit, offset)
}

// --- Restaurant (outlet) ---

// CreateRestaurantInput is the validated input for provisioning an outlet.
type CreateRestaurantInput struct{ OwnerID, BrandID, Name, Address, Timezone, GSTIN string }

// CreateRestaurant reserves the "outlets" quota BEFORE persisting, resolves the
// owner from the brand (never trusting an owner id from the body), then stages the
// outlet.provisioned event. Releases the reservation if persistence fails.
func (a *App) CreateRestaurant(ctx context.Context, in CreateRestaurantInput) (domain.Restaurant, error) {
	if !ids.Valid(domain.PrefixBrand, in.BrandID) {
		return domain.Restaurant{}, fmt.Errorf("%w: brand_id is invalid", domain.ErrInvalid)
	}

	// Resolve the owning brand under the caller's tenant scope.
	brand, err := a.repo.GetBrand(ctx, in.OwnerID, in.BrandID)
	if err != nil {
		return domain.Restaurant{}, err
	}

	r, err := domain.NewRestaurant(brand.ID, brand.OwnerID, in.Name, in.Address, in.Timezone, in.GSTIN, a.now())
	if err != nil {
		return domain.Restaurant{}, err
	}

	// Quota gate: reserve one "outlets" slot, idempotent by the new outlet id.
	res, err := a.ents.ReserveQuota(ctx, brand.OwnerID, domain.QuotaOutlets, 1, r.ID)
	if err != nil {
		return domain.Restaurant{}, fmt.Errorf("reserve outlets quota: %w", err)
	}
	if !res.OK {
		return domain.Restaurant{}, quotaErr(domain.QuotaOutlets, res.UpgradeHint)
	}

	err = a.repo.Atomic(ctx, brand.OwnerID, func(tx ports.Tx) error {
		if err := tx.InsertRestaurant(ctx, r); err != nil {
			return err
		}
		return tx.StageEvent(ctx, EventOutletProvisioned, r.OwnerID, outletEvent(r))
	})
	if err != nil {
		_ = a.ents.ReleaseQuota(ctx, brand.OwnerID, domain.QuotaOutlets, 1, r.ID)
		return domain.Restaurant{}, err
	}
	return r, nil
}

// ListRestaurants returns a brand's outlets, paginated.
func (a *App) ListRestaurants(ctx context.Context, ownerID, brandID string, limit, offset int) ([]domain.Restaurant, int, error) {
	if !ids.Valid(domain.PrefixBrand, brandID) {
		return nil, 0, fmt.Errorf("%w: brand_id is invalid", domain.ErrInvalid)
	}
	limit = clampLimit(limit)
	return a.repo.ListRestaurants(ctx, ownerID, brandID, limit, offset)
}

// SetRestaurantActive toggles an outlet on/off.
func (a *App) SetRestaurantActive(ctx context.Context, ownerID, restaurantID string, active bool) (domain.Restaurant, error) {
	if !ids.Valid(domain.PrefixRestaurant, restaurantID) {
		return domain.Restaurant{}, fmt.Errorf("%w: restaurant_id is invalid", domain.ErrInvalid)
	}
	var out domain.Restaurant
	err := a.repo.Atomic(ctx, ownerID, func(tx ports.Tx) error {
		r, err := tx.GetRestaurant(ctx, restaurantID)
		if err != nil {
			return err
		}
		r.SetActive(active)
		if err := tx.UpdateRestaurant(ctx, r); err != nil {
			return err
		}
		out = r
		return nil
	})
	if err != nil {
		return domain.Restaurant{}, err
	}
	return out, nil
}

// --- helpers ---

const defaultPageSize = 50

func clampLimit(n int) int {
	if n <= 0 {
		return defaultPageSize
	}
	if n > 200 {
		return 200
	}
	return n
}

// quotaErr wraps ErrQuotaExceeded carrying the upgrade hint; the grpc adapter
// surfaces it as ResourceExhausted with the hint in the message.
func quotaErr(key, hint string) error {
	msg := fmt.Sprintf("%s quota reached", key)
	if hint != "" {
		msg += ": " + hint
	}
	return &QuotaError{Key: key, Hint: hint, msg: msg}
}

// QuotaError is a typed quota failure; errors.Is(err, domain.ErrQuotaExceeded) is true.
type QuotaError struct {
	Key  string
	Hint string
	msg  string
}

func (e *QuotaError) Error() string { return e.msg }
func (e *QuotaError) Is(target error) bool {
	return errors.Is(target, domain.ErrQuotaExceeded)
}

// event payloads (kept small + stable; consumers project these).

func brandEvent(b domain.Brand) map[string]any {
	return map[string]any{
		"brand_id":      b.ID,
		"owner_id":      b.OwnerID,
		"name":          b.Name,
		"primary_color": b.PrimaryColor,
		"created_at":    b.CreatedAt.Format(time.RFC3339),
	}
}

func outletEvent(r domain.Restaurant) map[string]any {
	return map[string]any{
		"restaurant_id": r.ID,
		"brand_id":      r.BrandID,
		"owner_id":      r.OwnerID,
		"name":          r.Name,
		"timezone":      r.Timezone,
		"active":        r.Active,
		"created_at":    r.CreatedAt.Format(time.RFC3339),
	}
}
