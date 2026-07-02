// Package domain holds the pure catalog model: categories + items, brand menu vs
// per-outlet overrides, dietary evaluation, and availability (86) rules. It imports
// NO infrastructure (no pgx, nats, connect). Rules live here; adapters map this
// to/from proto and SQL. (Ported from the proven Restorna Node catalog service.)
package domain

import (
	"errors"
	"strings"
	"time"

	"github.com/restorna/platform/pkg/ids"
	"github.com/restorna/platform/pkg/money"
)

// ID type prefixes (see CONVENTIONS.md: type-prefixed ULIDs).
const (
	PrefixItem     = "item"
	PrefixCategory = "cat"
	PrefixAsset    = "ast"
)

// Domain errors. The grpc adapter maps these to Connect codes via pkg/errors.
var (
	ErrInvalid  = errors.New("invalid argument")
	ErrNotFound = errors.New("not found")
)

// DietaryFlags are the boolean dietary markers an item may declare (a tag value
// > 0 means the item carries that flag). Mirrors the Node demo's DIETARY_FLAGS.
var DietaryFlags = []string{
	"dairy", "nuts", "fish", "meat", "egg", "gluten", "alcohol", "sugar", "spicy", "raw",
}

// Asset is a stored dish photo. Mirrors common.v1.Asset.
type Asset struct {
	ID          string
	URL         string
	ContentType string
}

// Category groups items into a course (Appetizers | Mains | Breads | Drinks).
type Category struct {
	ID   string
	Name string
	Sort int32
}

// Item is a brand-level menu item. `price`/`available` here are the BRAND defaults;
// an OutletOverride may change the effective price/availability at a specific
// outlet. Tags are dietary flags (dairy/nuts/gluten/...) stored as 0/1 markers.
type Item struct {
	ID          string
	CategoryID  string
	Name        string
	Description string
	Price       money.Money
	Veg         bool
	Tags        map[string]int32
	PrepMinutes int32
	Station     string // grill|tandoor|cold (kitchen routing hint)
	Available   bool
	Image       *Asset
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// OutletOverride is a per-restaurant override of a brand item's price and/or
// availability. A zero/absent override means "inherit the brand item".
type OutletOverride struct {
	ItemID       string
	RestaurantID string
	Price        *money.Money // nil = inherit brand price
	Available    bool         // effective availability at this outlet
	HasAvail     bool         // whether Available is meaningful (override set)
}

// NewCategory constructs and validates a category. An empty id mints a new one
// (upsert-create); a supplied valid id is reused (upsert-update).
func NewCategory(id, name string, sort int32) (Category, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Category{}, fieldErr("name is required")
	}
	if id == "" {
		id = ids.New(PrefixCategory)
	} else if !ids.Valid(PrefixCategory, id) {
		return Category{}, fieldErr("category id is invalid")
	}
	return Category{ID: id, Name: name, Sort: sort}, nil
}

// NewItemInput is the validated construction input for a brand item.
type NewItemInput struct {
	ID          string
	CategoryID  string
	Name        string
	Description string
	Price       money.Money
	Veg         bool
	Tags        map[string]int32
	PrepMinutes int32
	Station     string
	Image       *Asset
}

// NewItem constructs and validates a brand item. New items default to available.
// An empty id mints a new ULID; a supplied valid item id is reused (update path).
func NewItem(in NewItemInput, now time.Time) (Item, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return Item{}, fieldErr("name is required")
	}
	if in.Price.Minor <= 0 {
		return Item{}, fieldErr("price must be a positive integer minor amount")
	}
	if in.Price.Currency == "" {
		in.Price.Currency = "INR"
	}
	id := in.ID
	if id == "" {
		id = ids.New(PrefixItem)
	} else if !ids.Valid(PrefixItem, id) {
		return Item{}, fieldErr("item id is invalid")
	}
	prep := in.PrepMinutes
	if prep < 0 {
		prep = 0
	}
	return Item{
		ID:          id,
		CategoryID:  strings.TrimSpace(in.CategoryID),
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		Price:       in.Price,
		Veg:         in.Veg,
		Tags:        normalizeTags(in.Tags),
		PrepMinutes: prep,
		Station:     strings.TrimSpace(in.Station),
		Available:   true, // brand items are available by default
		Image:       in.Image,
		CreatedAt:   now.UTC(),
		UpdatedAt:   now.UTC(),
	}, nil
}

// SetAvailability flips the item's availability (86 / un-86). Returns the changed
// item; ok reports whether the value actually changed.
func (it *Item) SetAvailability(available bool) (changed bool) {
	if it.Available == available {
		return false
	}
	it.Available = available
	return true
}

// HasFlag reports whether the item carries a dietary flag (value > 0).
func (it Item) HasFlag(flag string) bool {
	return it.Tags != nil && it.Tags[flag] > 0
}

// Effective applies an outlet override to a brand item, returning the item as it
// should appear AT THAT OUTLET: override price (if set) and override availability
// (if set). A nil override returns the brand item unchanged.
func (it Item) Effective(ov *OutletOverride) Item {
	if ov == nil {
		return it
	}
	out := it
	if ov.Price != nil {
		out.Price = *ov.Price
	}
	if ov.HasAvail {
		out.Available = ov.Available
	}
	return out
}

// --- Dietary preference engine (ported from Node demo's dietary.js) ---

// Pref is a named dietary preference: a set of flags to AVOID.
type Pref struct {
	ID    string
	Label string
	Avoid []string
}

// Prefs is the catalog of supported dietary preferences keyed by id.
var Prefs = map[string]Pref{
	"vegetarian": {"vegetarian", "Vegetarian", []string{"meat", "fish"}},
	"vegan":      {"vegan", "Vegan", []string{"meat", "fish", "dairy", "egg"}},
	"eggless":    {"eggless", "Eggless", []string{"egg"}},
	"pregnancy":  {"pregnancy", "Pregnancy-safe", []string{"fish", "alcohol", "raw"}},
	"glutenfree": {"glutenfree", "Gluten-free", []string{"gluten"}},
	"nutfree":    {"nutfree", "Nut-free", []string{"nuts"}},
	"lowsugar":   {"lowsugar", "Low-sugar", []string{"sugar"}},
	"mild":       {"mild", "Mild (not spicy)", []string{"spicy"}},
}

// Evaluation is the result of checking an item against active preferences.
// OK is true when the item is safe for ALL active prefs; Reasons explains conflicts.
type Evaluation struct {
	OK      bool
	Reasons []string
}

// Evaluate checks item against the active preference ids; an item violates a
// preference if it carries any flag the preference avoids. Unknown pref ids are
// ignored. Mirrors evaluateItem(item, activePrefIds) from the Node demo.
func Evaluate(it Item, activePrefIDs []string) Evaluation {
	var reasons []string
	for _, prefID := range activePrefIDs {
		pref, ok := Prefs[strings.TrimSpace(prefID)]
		if !ok {
			continue
		}
		for _, flag := range pref.Avoid {
			if it.HasFlag(flag) {
				reasons = append(reasons, it.Name+" contains "+flag+", not allowed for "+pref.Label)
			}
		}
	}
	return Evaluation{OK: len(reasons) == 0, Reasons: reasons}
}

// --- helpers ---

// normalizeTags collapses arbitrary tag values into clean 0/1 markers, dropping
// blank keys (matches the Node demo's normTags behaviour).
func normalizeTags(tags map[string]int32) map[string]int32 {
	out := make(map[string]int32, len(tags))
	for k, v := range tags {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		if v > 0 {
			out[k] = 1
		} else {
			out[k] = 0
		}
	}
	return out
}

func fieldErr(msg string) error { return errFmt{ErrInvalid, msg} }

// errFmt wraps a sentinel with a human message while keeping errors.Is working.
type errFmt struct {
	base error
	msg  string
}

func (e errFmt) Error() string { return e.base.Error() + ": " + e.msg }
func (e errFmt) Unwrap() error { return e.base }
