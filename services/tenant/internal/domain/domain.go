// Package domain holds the pure tenant model: Owner -> Brand -> Restaurant plus
// branding. It imports NO infrastructure (no pgx, nats, connect). Rules live here;
// adapters map this to/from proto and SQL.
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixOwner      = "own"
	PrefixBrand      = "brnd"
	PrefixRestaurant = "out"
	PrefixAsset      = "ast"
)

// Quota / feature keys checked against the EntitlementsService.
const (
	QuotaBrands     = "brands"
	QuotaOutlets    = "outlets"
	FeatureMultiBrand = "multi_brand"
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid       = errors.New("invalid argument")
	ErrNotFound      = errors.New("not found")
	ErrQuotaExceeded = errors.New("quota exceeded")
)

// Asset is a stored branding asset (logo). Mirrors common.v1.Asset.
type Asset struct {
	ID          string
	URL         string
	ContentType string
}

// Owner is the billable customer account at the top of the hierarchy.
type Owner struct {
	ID        string
	Name      string
	LegalName string
	Country   string
	CreatedAt time.Time
}

// Brand belongs to one Owner; one Owner may have many Brands.
type Brand struct {
	ID           string
	OwnerID      string
	Name         string
	Logo         *Asset
	PrimaryColor string
	CreatedAt    time.Time
}

// Restaurant (outlet) belongs to one Brand (and transitively one Owner).
type Restaurant struct {
	ID        string
	BrandID   string
	OwnerID   string
	Name      string
	Address   string
	Timezone  string
	GSTIN     string
	Logo      *Asset // outlet override; nil means inherit the brand logo
	Active    bool
	CreatedAt time.Time
}

// NewOwner constructs and validates a new Owner. country is required (ISO-3166).
func NewOwner(name, legalName, country string, now time.Time) (Owner, error) {
	name = strings.TrimSpace(name)
	country = strings.TrimSpace(country)
	if name == "" {
		return Owner{}, fieldErr("name is required")
	}
	if len(country) != 2 {
		return Owner{}, fieldErr("country must be a 2-letter ISO code")
	}
	if legalName = strings.TrimSpace(legalName); legalName == "" {
		legalName = name
	}
	return Owner{
		ID:        ids.New(PrefixOwner),
		Name:      name,
		LegalName: legalName,
		Country:   strings.ToUpper(country),
		CreatedAt: now.UTC(),
	}, nil
}

// NewBrand constructs and validates a new Brand under ownerID.
func NewBrand(ownerID, name, primaryColor string, now time.Time) (Brand, error) {
	if !ids.Valid(PrefixOwner, ownerID) {
		return Brand{}, fieldErr("owner_id is invalid")
	}
	if name = strings.TrimSpace(name); name == "" {
		return Brand{}, fieldErr("name is required")
	}
	if primaryColor = strings.TrimSpace(primaryColor); primaryColor != "" && !validHexColor(primaryColor) {
		return Brand{}, fieldErr("primary_color must be a hex color like #FF8800")
	}
	return Brand{
		ID:           ids.New(PrefixBrand),
		OwnerID:      ownerID,
		Name:         name,
		PrimaryColor: primaryColor,
		CreatedAt:    now.UTC(),
	}, nil
}

// NewRestaurant constructs and validates a new outlet under a brand. ownerID is the
// brand's owner (resolved by the caller from the brand, never trusted from input).
func NewRestaurant(brandID, ownerID, name, address, timezone, gstin string, now time.Time) (Restaurant, error) {
	if !ids.Valid(PrefixBrand, brandID) {
		return Restaurant{}, fieldErr("brand_id is invalid")
	}
	if !ids.Valid(PrefixOwner, ownerID) {
		return Restaurant{}, fieldErr("owner_id is invalid")
	}
	if name = strings.TrimSpace(name); name == "" {
		return Restaurant{}, fieldErr("name is required")
	}
	if timezone = strings.TrimSpace(timezone); timezone == "" {
		timezone = "Asia/Kolkata"
	}
	return Restaurant{
		ID:        ids.New(PrefixRestaurant),
		BrandID:   brandID,
		OwnerID:   ownerID,
		Name:      name,
		Address:   strings.TrimSpace(address),
		Timezone:  timezone,
		GSTIN:     strings.TrimSpace(gstin),
		Active:    true, // outlets are provisioned active
		CreatedAt: now.UTC(),
	}, nil
}

// SetLogo attaches/replaces the brand's branding asset.
func (b *Brand) SetLogo(a Asset) error {
	if a.URL == "" {
		return fieldErr("logo url is required")
	}
	b.Logo = &a
	return nil
}

// SetActive flips the outlet's active flag.
func (r *Restaurant) SetActive(active bool) { r.Active = active }

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }

func validHexColor(s string) bool {
	if len(s) != 4 && len(s) != 7 {
		return false
	}
	if s[0] != '#' {
		return false
	}
	for _, c := range s[1:] {
		isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
		if !isHex {
			return false
		}
	}
	return true
}
