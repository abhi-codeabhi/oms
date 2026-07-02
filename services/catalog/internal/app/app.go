// Package app holds the catalog use cases. It depends only on ports + domain. It
// orchestrates persistence (repo), per-outlet overrides, dietary evaluation, and
// event emission (outbox via Tx.StageEvent). The grpc adapter maps proto <-> these
// calls; tests drive it with in-memory fakes.
package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/restorna/platform/pkg/money"
	"github.com/restorna/platform/services/catalog/internal/domain"
	"github.com/restorna/platform/services/catalog/internal/ports"
)

// Event types emitted by this service (see CONVENTIONS.md naming).
const (
	EventItem86d       = "restorna.catalog.item.86d.v1"
	EventMenuPublished = "restorna.catalog.menu.published.v1"
)

// Now is the clock; overridable in tests for deterministic timestamps/ids.
type Now func() time.Time

// App is the use-case service (the hexagon's core application layer).
type App struct {
	repo ports.Repository
	now  Now
}

// New wires the app with its ports. now may be nil (defaults to time.Now).
func New(repo ports.Repository, now Now) *App {
	if now == nil {
		now = time.Now
	}
	return &App{repo: repo, now: now}
}

// --- Categories ---

// UpsertCategory creates or updates a course/category.
func (a *App) UpsertCategory(ctx context.Context, restaurantID, id, name string, sort int32) (domain.Category, error) {
	c, err := domain.NewCategory(id, name, sort)
	if err != nil {
		return domain.Category{}, err
	}
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		return tx.UpsertCategory(ctx, c)
	})
	if err != nil {
		return domain.Category{}, err
	}
	return c, nil
}

// ListCategories returns the outlet's categories ordered by sort then name.
func (a *App) ListCategories(ctx context.Context, restaurantID string) ([]domain.Category, error) {
	cats, err := a.repo.ListCategories(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(cats, func(i, j int) bool {
		if cats[i].Sort != cats[j].Sort {
			return cats[i].Sort < cats[j].Sort
		}
		return cats[i].Name < cats[j].Name
	})
	return cats, nil
}

// --- Items ---

// UpsertItemInput is the validated input for creating/updating a brand item.
type UpsertItemInput struct {
	ID          string
	CategoryID  string
	Name        string
	Description string
	Price       money.Money
	Veg         bool
	Tags        map[string]int32
	PrepMinutes int32
	Station     string
	Image       *domain.Asset
}

// UpsertItem creates a new brand item or updates an existing one (by id). On
// update the item's existing availability is preserved (availability is changed
// only via SetAvailability / SetOutletOverride, not a plain upsert).
func (a *App) UpsertItem(ctx context.Context, restaurantID string, in UpsertItemInput) (domain.Item, error) {
	item, err := domain.NewItem(domain.NewItemInput{
		ID:          in.ID,
		CategoryID:  in.CategoryID,
		Name:        in.Name,
		Description: in.Description,
		Price:       in.Price,
		Veg:         in.Veg,
		Tags:        in.Tags,
		PrepMinutes: in.PrepMinutes,
		Station:     in.Station,
		Image:       in.Image,
	}, a.now())
	if err != nil {
		return domain.Item{}, err
	}

	var out domain.Item
	err = a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		// Preserve availability + created_at on update.
		if in.ID != "" {
			if existing, gerr := tx.GetItem(ctx, in.ID); gerr == nil {
				item.Available = existing.Available
				item.CreatedAt = existing.CreatedAt
			}
		}
		if err := tx.UpsertItem(ctx, item); err != nil {
			return err
		}
		out = item
		// An upsert changes the published menu; emit menu.published so downstream
		// surfaces (customer menu cache, aggregators) re-pull. Version is the change
		// time (monotonic enough for a publish signal); item_count is filled by the
		// relay-side projection, so we send 0 here as a "menu changed" pulse.
		return tx.StageEvent(ctx, EventMenuPublished, restaurantID, menuPublishedEvent(restaurantID, int(a.now().Unix()), 0))
	})
	if err != nil {
		return domain.Item{}, err
	}
	return out, nil
}

// GetItem resolves a single item (effective price/availability at this outlet).
// Used by the order-flow saga to turn an order line into a name + station.
func (a *App) GetItem(ctx context.Context, restaurantID, itemID string) (domain.Item, error) {
	if itemID == "" {
		return domain.Item{}, fmt.Errorf("%w: item_id is required", domain.ErrInvalid)
	}
	return a.repo.GetItem(ctx, restaurantID, itemID)
}

// EvaluatedItem is an item plus its dietary evaluation against active prefs.
type EvaluatedItem struct {
	Item    domain.Item
	OK      bool
	Reasons []string
}

// GetMenu returns the customer menu: items with effective (post-override)
// availability. When onlyAvailable is true (the default for a customer menu),
// only available items are returned. When prefs are supplied, each item is
// evaluated and conflicting items are flagged (OK=false + reasons) but still
// returned so the surface can warn rather than hide.
func (a *App) GetMenu(ctx context.Context, restaurantID string, prefs []string, onlyAvailable bool) ([]EvaluatedItem, error) {
	items, err := a.repo.ListItems(ctx, restaurantID)
	if err != nil {
		return nil, err
	}
	out := make([]EvaluatedItem, 0, len(items))
	for _, it := range items {
		if onlyAvailable && !it.Available {
			continue
		}
		ev := domain.Evaluate(it, prefs)
		out = append(out, EvaluatedItem{Item: it, OK: ev.OK, Reasons: ev.Reasons})
	}
	return out, nil
}

// ListAllItems is the manager view: every item including unavailable ones, with
// effective per-outlet price/availability applied.
func (a *App) ListAllItems(ctx context.Context, restaurantID string) ([]domain.Item, error) {
	return a.repo.ListItems(ctx, restaurantID)
}

// SetAvailability 86s / un-86s an item AT THE OUTLET by writing a per-outlet
// availability override, then emits item.86d when the item goes unavailable.
// Returns the item with effective availability applied.
func (a *App) SetAvailability(ctx context.Context, restaurantID, itemID string, available bool) (domain.Item, error) {
	if itemID == "" {
		return domain.Item{}, fmt.Errorf("%w: item_id is required", domain.ErrInvalid)
	}
	var out domain.Item
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		item, err := tx.GetItem(ctx, itemID)
		if err != nil {
			return err
		}
		ov, _, err := tx.GetOverride(ctx, itemID)
		if err != nil {
			return err
		}
		ov.ItemID = itemID
		ov.RestaurantID = restaurantID
		ov.Available = available
		ov.HasAvail = true
		if err := tx.PutOverride(ctx, ov); err != nil {
			return err
		}
		out = item.Effective(&ov)
		// "86" — emit only when the item goes unavailable (out of stock / pulled).
		if !available {
			if err := tx.StageEvent(ctx, EventItem86d, restaurantID, item86dEvent(restaurantID, out)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return domain.Item{}, err
	}
	return out, nil
}

// SetOutletOverride sets (or clears) a per-outlet price/availability override of a
// brand item. clear=true removes the override (revert to brand defaults). Returns
// the item with the resulting effective values applied.
func (a *App) SetOutletOverride(ctx context.Context, restaurantID, itemID string, price *money.Money, available, clear bool) (domain.Item, error) {
	if itemID == "" {
		return domain.Item{}, fmt.Errorf("%w: item_id is required", domain.ErrInvalid)
	}
	if price != nil && price.Minor <= 0 {
		return domain.Item{}, fmt.Errorf("%w: override price must be positive", domain.ErrInvalid)
	}
	var out domain.Item
	err := a.repo.Atomic(ctx, restaurantID, func(tx ports.Tx) error {
		item, err := tx.GetItem(ctx, itemID)
		if err != nil {
			return err
		}
		if clear {
			if err := tx.ClearOverride(ctx, itemID); err != nil {
				return err
			}
			out = item // brand defaults
			return nil
		}
		ov := domain.OutletOverride{
			ItemID:       itemID,
			RestaurantID: restaurantID,
			Price:        price,
			Available:    available,
			HasAvail:     true,
		}
		if err := tx.PutOverride(ctx, ov); err != nil {
			return err
		}
		// 86 semantics: if the override takes the item offline, emit item.86d.
		if !available {
			if err := tx.StageEvent(ctx, EventItem86d, restaurantID, item86dEvent(restaurantID, item.Effective(&ov))); err != nil {
				return err
			}
		}
		out = item.Effective(&ov)
		return nil
	})
	if err != nil {
		return domain.Item{}, err
	}
	return out, nil
}

// --- event payloads (kept small + stable; consumers project these) ---

func item86dEvent(restaurantID string, it domain.Item) map[string]any {
	return map[string]any{
		"item_id":       it.ID,
		"restaurant_id": restaurantID,
		"name":          it.Name,
		"station":       it.Station,
	}
}

func menuPublishedEvent(restaurantID string, version, itemCount int) map[string]any {
	return map[string]any{
		"restaurant_id": restaurantID,
		"version":       version,
		"item_count":    itemCount,
	}
}
